// Copyright Contributors to the Open Cluster Management project
package e2e_test

import (
	"context"
	"fmt"
	"time"

	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const (
	loopbackClusterName = "loopback"
	localClusterName    = "local-cluster"
)

var _ = ginkgo.Describe("Loopback registration and work management", func() {
	ginkgo.Context("self management", func() {
		ginkgo.It("should have a registered cluster local-cluster", func() {
			ginkgo.By("Approve csr", func() {
				gomega.Expect(approveCSR(localClusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.By("Waiting the local-cluster becomes available", func() {
				gomega.Expect(wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
					localCluster, err := hubClusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), localClusterName, metav1.GetOptions{})
					if err != nil {
						return false, err
					}

					if meta.IsStatusConditionTrue(localCluster.Status.Conditions, clusterv1.ManagedClusterConditionAvailable) {
						return true, nil
					}

					return false, nil
				})).ToNot(gomega.HaveOccurred())
			})
		})

		ginkgo.It("should be able to create a manifestwork in local-cluster", func() {
			workName := fmt.Sprintf("local-clsuter-%s", rand.String(6))
			configMapName := fmt.Sprintf("local-cluster-cm-%s", rand.String(6))
			createAndDeleteManifestwork(localClusterName, workName, configMapName)
		})
	})

	ginkgo.Context("cluster registration with controlplane agent", func() {
		ginkgo.It("should have a loopback cluster", func() {
			ginkgo.By("Waiting the loopback cluster", func() {
				gomega.Expect(wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
					_, err := hubClusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), loopbackClusterName, metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false, nil
					}

					if err != nil {
						return false, err
					}

					return true, nil
				})).ToNot(gomega.HaveOccurred())
			})

			ginkgo.By("Approve csr", func() {
				gomega.Expect(approveCSR(loopbackClusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.By("Accept the loopback cluster", func() {
				loopbackCluster, err := hubClusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), loopbackClusterName, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				loopbackCluster.Spec.HubAcceptsClient = true
				_, err = hubClusterClient.ClusterV1().ManagedClusters().Update(context.TODO(), loopbackCluster, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			})

			ginkgo.By("Waiting the loopback becomes available", func() {
				gomega.Expect(wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
					loopbackCluster, err := hubClusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), loopbackClusterName, metav1.GetOptions{})
					if err != nil {
						return false, err
					}

					if meta.IsStatusConditionTrue(loopbackCluster.Status.Conditions, clusterv1.ManagedClusterConditionAvailable) {
						return true, nil
					}

					return false, nil
				})).ToNot(gomega.HaveOccurred())
			})
		})

		ginkgo.It("should be able to create a manifestwork in loopback", func() {
			workName := fmt.Sprintf("loopback-%s", rand.String(6))
			configMapName := fmt.Sprintf("loopback-cm-%s", rand.String(6))
			createAndDeleteManifestwork(loopbackClusterName, workName, configMapName)
		})
	})
})

func createAndDeleteManifestwork(clusterName, workName, configMapName string) {
	ginkgo.By(fmt.Sprintf("Create a manifestwork %q in the cluster %q", workName, clusterName), func() {
		_, err := hubWorkClient.WorkV1().ManifestWorks(clusterName).Create(
			context.TODO(),
			&workv1.ManifestWork{
				ObjectMeta: metav1.ObjectMeta{
					Name: workName,
				},
				Spec: workv1.ManifestWorkSpec{
					Workload: workv1.ManifestsTemplate{
						Manifests: []workv1.Manifest{
							toManifest(newConfigmap(configMapName)),
						},
					},
				},
			},
			metav1.CreateOptions{},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.By("Waiting the manifestwork becomes available", func() {
		gomega.Expect(wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
			work, err := hubWorkClient.WorkV1().ManifestWorks(clusterName).Get(context.TODO(), workName, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}

			if meta.IsStatusConditionTrue(work.Status.Conditions, workv1.WorkAvailable) {
				return true, nil
			}

			return false, nil
		})).ToNot(gomega.HaveOccurred())
	})

	ginkgo.By("Get the configmap that was created by manifestwork", func() {
		_, err := spokeKubeClient.CoreV1().ConfigMaps(defaultNamespace).Get(context.TODO(), configMapName, metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.By("Delete the manifestwork from local-cluster", func() {
		err := hubWorkClient.WorkV1().ManifestWorks(clusterName).Delete(context.TODO(), workName, metav1.DeleteOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.By("Waiting the configmap is deleted", func() {
		gomega.Expect(wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
			_, err := spokeKubeClient.CoreV1().ConfigMaps(defaultNamespace).Get(context.TODO(), configMapName, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				return true, nil
			}

			if err != nil {
				return false, err
			}

			return false, nil
		})).ToNot(gomega.HaveOccurred())
	})
}
