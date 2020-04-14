package nsnetworkpolicy

import (
	"fmt"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubeinformers "k8s.io/client-go/informers"
	informerv1 "k8s.io/client-go/informers/core/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	netv1alpha1 "kubesphere.io/kubesphere/pkg/apis/network/v1alpha1"
	wkspv1alpha1 "kubesphere.io/kubesphere/pkg/apis/tenant/v1alpha1"
	ksfake "kubesphere.io/kubesphere/pkg/client/clientset/versioned/fake"
	ksinformers "kubesphere.io/kubesphere/pkg/client/informers/externalversions"
	nsnppolicyinformer "kubesphere.io/kubesphere/pkg/client/informers/externalversions/network/v1alpha1"
	workspaceinformer "kubesphere.io/kubesphere/pkg/client/informers/externalversions/tenant/v1alpha1"
	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/controller/network/provider"
	"reflect"
	"strings"
)

var (
	c                 *NamespacenpController
	stopCh            chan struct{}
	nsnpInformer      nsnppolicyinformer.NamespaceNetworkPolicyInformer
	serviceInformer   informerv1.ServiceInformer
	workspaceInformer workspaceinformer.WorkspaceInformer
	namespaceInformer informerv1.NamespaceInformer
	alwaysReady       = func() bool { return true }
)

const (
	workspaceNP = `
apiVersion: "networking.k8s.io/v1"
kind: "NetworkPolicy"
metadata:
  name: networkisolate
  namespace: %s
spec:
  podSelector: {}
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              %s: %s`

	serviceTmp = `
apiVersion: v1
kind: Service
metadata:
  name: myservice
  namespace: testns
spec:
  selector:
    app: mylbapp
  ports:
    - protocol: TCP
      port: 80
      targetPort: 9376
`

	workspaceTmp = `
apiVersion: tenant.kubesphere.io/v1alpha1
kind: Workspace
metadata:
  annotations:
    kubesphere.io/creator: admin
  name: testworkspace
spec:
  manager: admin
  networkIsolation: true
status: {}
`

	nsTmp = `
apiVersion: v1
kind: Namespace
metadata:
  labels:
    kubesphere.io/workspace: testworkspace
  name: testns
spec:
  finalizers:
  - kubernetes
`
)

func StringToObject(data string, obj interface{}) error {
	reader := strings.NewReader(data)
	return yaml.NewYAMLOrJSONDecoder(reader, 10).Decode(obj)
}

var _ = Describe("Nsnetworkpolicy", func() {
	BeforeEach(func() {
		stopCh = make(chan struct{})
		calicoProvider := provider.NewFakeNetworkProvider()

		kubeClient := kubefake.NewSimpleClientset()
		ksClient := ksfake.NewSimpleClientset()
		kubeInformer := kubeinformers.NewSharedInformerFactory(kubeClient, 0)
		ksInformer := ksinformers.NewSharedInformerFactory(ksClient, 0)

		nsnpInformer := ksInformer.Network().V1alpha1().NamespaceNetworkPolicies()
		serviceInformer := kubeInformer.Core().V1().Services()
		workspaceInformer := ksInformer.Tenant().V1alpha1().Workspaces()
		namespaceInformer := kubeInformer.Core().V1().Namespaces()

		c = NewnamespacenpController(kubeClient, ksClient.NetworkV1alpha1(), nsnpInformer, serviceInformer, workspaceInformer, namespaceInformer, calicoProvider)

		ksInformer.Start(stopCh)
		kubeInformer.Start(stopCh)

		c.namespaceInformerSynced = alwaysReady
		c.serviceInformerSynced = alwaysReady
		c.workspaceInformerSynced = alwaysReady
		c.informerSynced = alwaysReady

		serviceObj := &corev1.Service{}
		Expect(StringToObject(serviceTmp, serviceObj)).ShouldNot(HaveOccurred())
		serviceInformer.Informer().GetIndexer().Add(serviceObj)
		nsObj := &corev1.Namespace{}
		Expect(StringToObject(nsTmp, nsObj)).ShouldNot(HaveOccurred())
		namespaceInformer.Informer().GetIndexer().Add(nsObj)
		workspaceObj := &wkspv1alpha1.Workspace{}
		Expect(StringToObject(workspaceTmp, workspaceObj)).ShouldNot(HaveOccurred())
		workspaceInformer.Informer().GetIndexer().Add(workspaceObj)

		go c.Start(stopCh)
	})

	It("Should create ns networkisolate np correctly in workspace", func() {
		objSrt := fmt.Sprintf(workspaceNP, "testns", constants.WorkspaceLabelKey, "testworkspace")
		obj := &netv1.NetworkPolicy{}
		Expect(StringToObject(objSrt, obj)).ShouldNot(HaveOccurred())

		policy := nsNP("testworkspace", "testns", true)
		Expect(reflect.DeepEqual(obj.Spec, policy.Spec)).To(BeTrue())
	})

	It("Should create ns networkisolate np correctly in ns", func() {
		objSrt := fmt.Sprintf(workspaceNP, "testns", NamespaceLabelKey, "testns")
		obj := &netv1.NetworkPolicy{}
		Expect(StringToObject(objSrt, obj)).ShouldNot(HaveOccurred())

		policy := nsNP("testworkspace", "testns", false)
		Expect(reflect.DeepEqual(obj.Spec, policy.Spec)).To(BeTrue())
	})

	It("test func convertToK8sNp", func() {
		objSrt := `
apiVersion: networking.kubesphere.io/v1alpha1
kind: NamespaceNetworkPolicy
metadata:
  name: namespaceIPblockNP
  namespace: testns
spec:
  ingress:
  - from:
    - ipBlock:
        cidr: 172.0.0.1/16
    ports:
    - protocol: TCP
      port: 80
`
		obj := &netv1alpha1.NamespaceNetworkPolicy{}
		Expect(StringToObject(objSrt, obj)).ShouldNot(HaveOccurred())
		policy, err := c.convertToK8sNp(obj)

		objSrt = `
apiVersion: "networking.k8s.io/v1"
kind: "NetworkPolicy"
metadata:
  name: IPblockNP
  namespace: testns
spec:
  ingress:
  - from:
    - ipBlock:
        cidr: 172.0.0.1/16
    ports:
    - protocol: TCP
      port: 80
`
		obj2 := &netv1.NetworkPolicy{}
		Expect(StringToObject(objSrt, obj2)).ShouldNot(HaveOccurred())
		Expect(err).ShouldNot(HaveOccurred())
		Expect(reflect.DeepEqual(obj2.Spec, policy.Spec)).To(BeTrue())
	})

	It("test func convertToK8sNp with namespace", func() {
		objSrt := `
apiVersion: networking.kubesphere.io/v1alpha1
kind: NamespaceNetworkPolicy
metadata:
  name: testnamespace
  namespace: testns2
spec:
  ingress:
  - from:
    - namespace:
        name: testns
`
		obj := &netv1alpha1.NamespaceNetworkPolicy{}
		Expect(StringToObject(objSrt, obj)).ShouldNot(HaveOccurred())

		np, err := c.convertToK8sNp(obj)
		Expect(err).ShouldNot(HaveOccurred())

		objSrt = fmt.Sprintf(workspaceNP, "testns", NamespaceLabelKey, "testns")
		obj2 := &netv1.NetworkPolicy{}
		Expect(StringToObject(objSrt, obj2)).ShouldNot(HaveOccurred())
		Expect(reflect.DeepEqual(np.Spec, obj2.Spec)).To(BeTrue())
	})

	It("test func convertToK8sNp with service", func() {
		objSrt := `
apiVersion: networking.kubesphere.io/v1alpha1
kind: NamespaceNetworkPolicy
metadata:
  name: testnamespace
  namespace: testns2
spec:
  ingress:
  - from:
    - service:
        name: myservice
        namespace: testns
`
		obj := &netv1alpha1.NamespaceNetworkPolicy{}
		Expect(StringToObject(objSrt, obj)).ShouldNot(HaveOccurred())

		np, err := c.convertToK8sNp(obj)
		Expect(err).ShouldNot(HaveOccurred())

		objSrt = `
apiVersion: "networking.k8s.io/v1"
kind: NetworkPolicy
metadata:
  name: networkisolate
  namespace: testns
spec:
  podSelector: {}
  ingress:
    - from:
        - podSelector:
            matchLabels:
             app: mylbapp
          namespaceSelector:
            matchLabels:
              kubesphere.io/namespace: testns`
		obj2 := &netv1.NetworkPolicy{}
		Expect(StringToObject(objSrt, obj2)).ShouldNot(HaveOccurred())
		Expect(reflect.DeepEqual(np.Spec, obj2.Spec)).To(BeTrue())
	})

	AfterEach(func() {
		close(stopCh)
	})
})
