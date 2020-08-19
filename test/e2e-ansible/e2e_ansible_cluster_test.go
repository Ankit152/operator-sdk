// Copyright 2020 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writifng, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e_ansible_test

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kbtestutils "sigs.k8s.io/kubebuilder/test/e2e/utils"

	testutils "github.com/operator-framework/operator-sdk/test/internal"
)

var _ = Describe("Running ansible projects", func() {
	var controllerPodName string
	var memcachedSampleFile string
	var fooSampleFile string
	var memfinSampleFile string
	var memcachedDeployment string

	Context("built with operator-sdk", func() {
		BeforeEach(func() {
			By("checking samples")
			memcachedSampleFile = filepath.Join(tc.Dir, "config", "samples",
				fmt.Sprintf("%s_%s_%s.yaml", tc.Group, tc.Version, strings.ToLower(tc.Kind)))
			fooSampleFile = filepath.Join(tc.Dir, "config", "samples",
				fmt.Sprintf("%s_%s_foo.yaml", tc.Group, tc.Version))
			memfinSampleFile = filepath.Join(tc.Dir, "config", "samples",
				fmt.Sprintf("%s_%s_memfin.yaml", tc.Group, tc.Version))

			By("enabling Prometheus via the kustomization.yaml")
			Expect(kbtestutils.UncommentCode(
				filepath.Join(tc.Dir, "config", "default", "kustomization.yaml"),
				"#- ../prometheus", "#")).To(Succeed())

			By("deploying project on the cluster")
			err := tc.Make("deploy", "IMG="+tc.ImageName)
			Expect(err).Should(Succeed())
		})
		AfterEach(func() {
			By("deleting Curl Pod created")
			_, _ = tc.Kubectl.Delete(false, "pod", "curl")

			By("deleting CR instances created")
			_, _ = tc.Kubectl.Delete(false, "-f", memcachedSampleFile)
			_, _ = tc.Kubectl.Delete(false, "-f", fooSampleFile)
			_, _ = tc.Kubectl.Delete(false, "-f", memfinSampleFile)

			By("cleaning up permissions")
			_, _ = tc.Kubectl.Command("delete", "clusterrolebinding",
				fmt.Sprintf("metrics-%s", tc.TestSuffix))

			By("undeploy project")
			_ = tc.Make("undeploy")

			By("ensuring that the namespace was deleted")
			verifyNamespaceDeleted := func() error {
				_, err := tc.Kubectl.Command("get", "namespace", tc.Kubectl.Namespace)
				if strings.Contains(err.Error(), "(NotFound): namespaces") {
					return err
				}
				return nil
			}
			Eventually(verifyNamespaceDeleted, 2*time.Minute, time.Second).ShouldNot(Succeed())
		})

		It("should run correctly in a cluster", func() {
			By("checking if the Operator project Pod is running")
			verifyControllerUp := func() error {
				By("getting the controller-manager pod name")
				podOutput, err := tc.Kubectl.Get(
					true,
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}")
				Expect(err).NotTo(HaveOccurred())

				By("ensuring the created controller-manager Pod")
				podNames := kbtestutils.GetNonEmptyLines(podOutput)
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
				}
				controllerPodName = podNames[0]
				Expect(controllerPodName).Should(ContainSubstring("controller-manager"))

				By("checking the controller-manager Pod is running")
				status, err := tc.Kubectl.Get(
					true,
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}")
				Expect(err).NotTo(HaveOccurred())
				if status != "Running" {
					return fmt.Errorf("controller pod in %s status", status)
				}
				return nil
			}
			Eventually(verifyControllerUp, 2*time.Minute, time.Second).Should(Succeed())

			By("ensuring the created ServiceMonitor for the manager")
			_, err := tc.Kubectl.Get(
				true,
				"ServiceMonitor",
				fmt.Sprintf("e2e-%s-controller-manager-metrics-monitor", tc.TestSuffix))
			Expect(err).NotTo(HaveOccurred())

			By("ensuring the created metrics Service for the manager")
			_, err = tc.Kubectl.Get(
				true,
				"Service",
				fmt.Sprintf("e2e-%s-controller-manager-metrics-service", tc.TestSuffix))
			Expect(err).NotTo(HaveOccurred())

			By("create custom resource (Memcached CR)")
			_, err = tc.Kubectl.Apply(false, "-f", memcachedSampleFile)
			Expect(err).NotTo(HaveOccurred())

			By("create custom resource (Foo CR)")
			_, err = tc.Kubectl.Apply(false, "-f", fooSampleFile)
			Expect(err).NotTo(HaveOccurred())

			By("create custom resource (Memfin CR)")
			_, err = tc.Kubectl.Apply(false, "-f", memfinSampleFile)
			Expect(err).NotTo(HaveOccurred())

			By("ensuring the CR gets reconciled")
			managerContainerLogs := func() string {
				logOutput, err := tc.Kubectl.Logs(controllerPodName, "-c", "manager")
				Expect(err).NotTo(HaveOccurred())
				return logOutput
			}
			Eventually(managerContainerLogs, time.Minute, time.Second).Should(ContainSubstring(
				"Ansible-runner exited successfully"))
			Eventually(managerContainerLogs, time.Minute, time.Second).ShouldNot(ContainSubstring("failed=1"))

			By("ensuring no liveness probe fail events")
			verifyControllerProbe := func() string {
				By("getting the controller-manager events")
				eventsOutput, err := tc.Kubectl.Get(
					true,
					"events", "--field-selector", fmt.Sprintf("involvedObject.name=%s", controllerPodName))
				Expect(err).NotTo(HaveOccurred())
				return eventsOutput
			}
			Eventually(verifyControllerProbe, time.Minute, time.Second).ShouldNot(ContainSubstring("Killing"))

			By("getting memcached deploy by labels")
			getMencachedDeploument := func() string {
				memcachedDeployment, err = tc.Kubectl.Get(
					false, "deployment",
					"-l", "app=memcached", "-o", "jsonpath={..metadata.name}")
				Expect(err).NotTo(HaveOccurred())
				return memcachedDeployment
			}
			Eventually(getMencachedDeploument, 2*time.Minute, time.Second).ShouldNot(BeEmpty())

			By("checking the Memcached CR deployment status")
			verifyCRUp := func() string {
				output, err := tc.Kubectl.Command(
					"rollout", "status", "deployment", memcachedDeployment)
				Expect(err).NotTo(HaveOccurred())
				return output
			}
			Eventually(verifyCRUp, time.Minute, time.Second).Should(ContainSubstring("successfully rolled out"))

			By("ensuring the created Service for the Memcached CR")
			crServiceName, err := tc.Kubectl.Get(
				false,
				"Service", "-l", "app=memcached")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(crServiceName)).NotTo(BeIdenticalTo(0))

			By("Verifying that a config map owned by the CR has been created")
			verifyConfigMap := func() error {
				_, err = tc.Kubectl.Get(
					false,
					"configmap", "test-blacklist-watches")
				return err
			}
			Eventually(verifyConfigMap, time.Minute*2, time.Second).Should(Succeed())

			By("Ensuring that config map requests skip the cache.")
			checkSkipCache := func() string {
				logOutput, err := tc.Kubectl.Logs(controllerPodName, "-c", "manager")
				Expect(err).NotTo(HaveOccurred())
				return logOutput
			}
			Eventually(checkSkipCache, time.Minute, time.Second).Should(ContainSubstring("\"Skipping cache " +
				"lookup\",\"resource\":{\"IsResourceRequest\":true," +
				"\"Path\":\"/api/v1/namespaces/default/configmaps/test-blacklist-watches\""))

			By("scaling deployment replicas to 2")
			_, err = tc.Kubectl.Command(
				"scale", "deployment", memcachedDeployment, "--replicas", "2")
			Expect(err).NotTo(HaveOccurred())

			By("verifying the deployment automatically scales back down to 1")
			verifyMemcachedScalesBack := func() error {
				replicas, err := tc.Kubectl.Get(
					false,
					"deployment", memcachedDeployment, "-o", "jsonpath={..spec.replicas}")
				Expect(err).NotTo(HaveOccurred())
				if replicas != "1" {
					return fmt.Errorf("memcached(CR) deployment with %s replicas", replicas)
				}
				return nil
			}
			Eventually(verifyMemcachedScalesBack, time.Minute, time.Second).Should(Succeed())

			By("updating size to 2 in the CR manifest")
			testutils.ReplaceInFile(memcachedSampleFile, "size: 1", "size: 2")

			By("applying CR manifest with size: 2")
			_, err = tc.Kubectl.Apply(false, "-f", memcachedSampleFile)
			Expect(err).NotTo(HaveOccurred())

			By("ensuring the CR gets reconciled after patching it")
			managerContainerLogsAfterUpdateCR := func() string {
				logOutput, err := tc.Kubectl.Logs(controllerPodName, "-c", "manager")
				Expect(err).NotTo(HaveOccurred())
				return logOutput
			}
			Eventually(managerContainerLogsAfterUpdateCR, time.Minute, time.Second).Should(
				ContainSubstring("Ansible-runner exited successfully"))
			Eventually(managerContainerLogs, time.Minute, time.Second).ShouldNot(ContainSubstring("failed=1"))

			By("checking Deployment replicas spec is equals 2")
			verifyMemcachedPatch := func() error {
				replicas, err := tc.Kubectl.Get(
					false,
					"deployment", memcachedDeployment, "-o", "jsonpath={..spec.replicas}")
				Expect(err).NotTo(HaveOccurred())
				if replicas != "2" {
					return fmt.Errorf("memcached(CR) deployment with %s replicas", replicas)
				}
				return nil
			}
			Eventually(verifyMemcachedPatch, time.Minute, time.Second).Should(Succeed())

			By("granting permissions to access the metrics and read the token")
			_, err = tc.Kubectl.Command(
				"create",
				"clusterrolebinding",
				fmt.Sprintf("metrics-%s", tc.TestSuffix),
				fmt.Sprintf("--clusterrole=e2e-%s-metrics-reader", tc.TestSuffix),
				fmt.Sprintf("--serviceaccount=%s:default", tc.Kubectl.Namespace))
			Expect(err).NotTo(HaveOccurred())

			By("getting the token")
			b64Token, err := tc.Kubectl.Get(
				true,
				"secrets",
				"-o=jsonpath={.items[0].data.token}")
			Expect(err).NotTo(HaveOccurred())
			token, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64Token))
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(HaveLen(0))

			By("creating a pod with curl image")
			// todo: the flag --generator=run-pod/v1 is deprecated, however, shows that besides
			// it should not make any difference and work locally successfully when the flag is removed
			// travis has been failing and the curl pod is not found when the flag is not used
			cmdOpts := []string{
				"run", "--generator=run-pod/v1", "curl", "--image=curlimages/curl:7.68.0", "--restart=OnFailure", "--",
				"curl", "-v", "-k", "-H", fmt.Sprintf(`Authorization: Bearer %s`, token),
				fmt.Sprintf("https://e2e-%v-controller-manager-metrics-service.e2e-%v-system.svc:8443/metrics",
					tc.TestSuffix, tc.TestSuffix),
			}
			_, err = tc.Kubectl.CommandInNamespace(cmdOpts...)
			Expect(err).NotTo(HaveOccurred())

			By("validating the curl pod running as expected")
			verifyCurlUp := func() error {
				// Validate pod status
				status, err := tc.Kubectl.Get(
					true,
					"pods", "curl", "-o", "jsonpath={.status.phase}")
				Expect(err).NotTo(HaveOccurred())
				if status != "Completed" && status != "Succeeded" {
					return fmt.Errorf("curl pod in %s status", status)
				}
				return nil
			}
			Eventually(verifyCurlUp, 4*time.Minute, time.Second).Should(Succeed())

			By("checking metrics endpoint serving as expected")
			getCurlLogs := func() string {
				logOutput, err := tc.Kubectl.Logs("curl")
				Expect(err).NotTo(HaveOccurred())
				return logOutput
			}
			Eventually(getCurlLogs, time.Minute, time.Second).Should(ContainSubstring("< HTTP/2 200"))

			By("getting the CR namespace token")
			crNamespace, err := tc.Kubectl.Get(
				false,
				tc.Kind,
				fmt.Sprintf("%s-sample", strings.ToLower(tc.Kind)),
				"-o=jsonpath={..metadata.namespace}")
			Expect(err).NotTo(HaveOccurred())
			Expect(crNamespace).NotTo(HaveLen(0))

			By("ensuring the operator metrics contains a `resource_created_at` metric for the Memcached CR")
			metricExportedMemcachedCR := fmt.Sprintf("resource_created_at_seconds{group=\"%s\","+
				"kind=\"%s\","+
				"name=\"%s-sample\","+
				"namespace=\"%s\","+
				"version=\"%s\"}",
				fmt.Sprintf("%s.%s", tc.Group, tc.Domain),
				tc.Kind,
				strings.ToLower(tc.Kind),
				crNamespace,
				tc.Version)
			Eventually(getCurlLogs, time.Minute, time.Second).Should(ContainSubstring(metricExportedMemcachedCR))

			By("ensuring the operator metrics contains a `resource_created_at` metric for the Foo CR")
			metricExportedFooCR := fmt.Sprintf("resource_created_at_seconds{group=\"%s\","+
				"kind=\"%s\","+
				"name=\"%s-sample\","+
				"namespace=\"%s\","+
				"version=\"%s\"}",
				fmt.Sprintf("%s.%s", tc.Group, tc.Domain),
				"Foo",
				strings.ToLower("Foo"),
				crNamespace,
				tc.Version)
			Eventually(getCurlLogs, time.Minute, time.Second).Should(ContainSubstring(metricExportedFooCR))

			By("ensuring the operator metrics contains a `resource_created_at` metric for the Memfin CR")
			metricExportedMemfinCR := fmt.Sprintf("resource_created_at_seconds{group=\"%s\","+
				"kind=\"%s\","+
				"name=\"%s-sample\","+
				"namespace=\"%s\","+
				"version=\"%s\"}",
				fmt.Sprintf("%s.%s", tc.Group, tc.Domain),
				"Memfin",
				strings.ToLower("Memfin"),
				crNamespace,
				tc.Version)
			Eventually(getCurlLogs, time.Minute, time.Second).Should(ContainSubstring(metricExportedMemfinCR))

			By("creating a configmap that the finalizer should remove")
			_, err = tc.Kubectl.Command("create", "configmap", "deleteme")
			Expect(err).NotTo(HaveOccurred())

			By("deleting Memcached CR manifest")
			_, err = tc.Kubectl.Delete(false, "-f", memcachedSampleFile)
			Expect(err).NotTo(HaveOccurred())

			By("ensuring the CR gets reconciled successfully")
			managerContainerLogsAfterDeleteCR := func() string {
				logOutput, err := tc.Kubectl.Logs(controllerPodName, "-c", "manager")
				Expect(err).NotTo(HaveOccurred())
				return logOutput
			}
			Eventually(managerContainerLogsAfterDeleteCR, time.Minute, time.Second).Should(ContainSubstring(
				"Ansible-runner exited successfully"))
			Eventually(managerContainerLogsAfterDeleteCR).ShouldNot(ContainSubstring("error"))

			By("ensuring that Memchaced Deployment was removed")
			getMemcachedDeployment := func() error {
				_, err := tc.Kubectl.Get(
					false, "deployment",
					memcachedDeployment)
				return err
			}
			Eventually(getMemcachedDeployment, time.Minute*2, time.Second).ShouldNot(Succeed())
		})
	})
})
