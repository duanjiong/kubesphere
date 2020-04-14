package nsnetworkpolicy

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typev1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	uruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	"kubesphere.io/kubesphere/pkg/apis/network/v1alpha1"
	workspacev1alpha1 "kubesphere.io/kubesphere/pkg/apis/tenant/v1alpha1"
	ksnetclient "kubesphere.io/kubesphere/pkg/client/clientset/versioned/typed/network/v1alpha1"
	nspolicy "kubesphere.io/kubesphere/pkg/client/informers/externalversions/network/v1alpha1"
	workspace "kubesphere.io/kubesphere/pkg/client/informers/externalversions/tenant/v1alpha1"
	"kubesphere.io/kubesphere/pkg/constants"
	"kubesphere.io/kubesphere/pkg/controller/network/provider"
	"time"
)

const (
	defaultSleepDuration         = 60 * time.Second
	defaultThread                = 5
	defaultSync                  = "5m"
	NamespaceLabelKey            = "kubesphere.io/namespace"
	NamespaceNPAnnotationKey     = "kubesphere.io/networkisolate"
	NamespaceNPAnnotationEnabled = "enabled"
	AnnotationNPNAME             = "networkisolate"
)

// namespacenpController implements the Controller interface for managing kubesphere network policies
// and convery them to k8s NetworkPolicies, then syncing them to the provider.
type NamespacenpController struct {
	client         kubernetes.Interface
	ksclient       ksnetclient.NetworkV1alpha1Interface
	informer       nspolicy.NamespaceNetworkPolicyInformer
	informerSynced cache.InformerSynced

	serviceInformer       v1.ServiceInformer
	serviceInformerSynced cache.InformerSynced

	workspaceInformer       workspace.WorkspaceInformer
	workspaceInformerSynced cache.InformerSynced

	namespaceInformer       v1.NamespaceInformer
	namespaceInformerSynced cache.InformerSynced

	provider provider.NsNetworkPolicyProvider

	nsQueue   workqueue.RateLimitingInterface
	nsnpQueue workqueue.RateLimitingInterface
}

func (c *NamespacenpController) convertPeer(peers []v1alpha1.NetworkPolicyPeer) ([]netv1.NetworkPolicyPeer, error) {
	rules := make([]netv1.NetworkPolicyPeer, 0)

	for _, peer := range peers {
		rule := netv1.NetworkPolicyPeer{}

		if peer.ServiceSelector != nil {
			rule.PodSelector = new(metav1.LabelSelector)
			rule.NamespaceSelector = new(metav1.LabelSelector)

			namespace := peer.ServiceSelector.Namespace
			name := peer.ServiceSelector.Name
			service, err := c.serviceInformer.Lister().Services(namespace).Get(name)
			if err != nil {
				return nil, err
			}

			if len(service.Spec.Selector) <= 0 {
				return nil, fmt.Errorf("service %s:%s has no podselect", namespace, name)
			}

			rule.PodSelector.MatchLabels = make(map[string]string)
			for key, value := range service.Spec.Selector {
				rule.PodSelector.MatchLabels[key] = value
			}
			rule.NamespaceSelector.MatchLabels = make(map[string]string)
			rule.NamespaceSelector.MatchLabels[NamespaceLabelKey] = namespace
		} else if peer.NamespaceSelector != nil {
			name := peer.NamespaceSelector.Name

			rule.NamespaceSelector = new(metav1.LabelSelector)
			rule.NamespaceSelector.MatchLabels = make(map[string]string)
			rule.NamespaceSelector.MatchLabels[NamespaceLabelKey] = name
		} else if peer.IPBlock != nil {
			rule.IPBlock = peer.IPBlock
		} else {
			klog.Errorf("Invalid nsnp peer %v\n", peer)
			continue
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

func (c *NamespacenpController) convertToK8sNp(n *v1alpha1.NamespaceNetworkPolicy) (*netv1.NetworkPolicy, error) {
	np := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              n.Name,
			Namespace:         n.Namespace,
			UID:               n.UID,
			CreationTimestamp: n.CreationTimestamp,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
		},
	}

	if n.Spec.Egress != nil {
		np.Spec.Egress = make([]netv1.NetworkPolicyEgressRule, len(n.Spec.Egress))
		for indexEgress, egress := range n.Spec.Egress {
			rules, err := c.convertPeer(egress.To)
			if err != nil {
				return nil, err
			}
			np.Spec.Egress[indexEgress].To = rules
			np.Spec.Egress[indexEgress].Ports = egress.Ports
		}
	}

	if n.Spec.Ingress != nil {
		np.Spec.Ingress = make([]netv1.NetworkPolicyIngressRule, len(n.Spec.Ingress))
		for indexIngress, ingress := range n.Spec.Ingress {
			rules, err := c.convertPeer(ingress.From)
			if err != nil {
				return nil, err
			}
			np.Spec.Ingress[indexIngress].From = rules
			np.Spec.Ingress[indexIngress].Ports = ingress.Ports
		}
	}

	return np, nil
}

func nsNP(workspace string, ns string, isworkspace bool) *netv1.NetworkPolicy {
	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AnnotationNPNAME,
			Namespace: ns,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			Ingress: []netv1.NetworkPolicyIngressRule{{
				From: []netv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{},
					},
				}},
			}},
		},
	}

	if isworkspace {
		policy.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[constants.WorkspaceLabelKey] = workspace
	} else {
		policy.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[NamespaceLabelKey] = ns
	}

	return policy
}

func (c *NamespacenpController) nsEnqueue(ns *corev1.Namespace) {
	key, err := cache.MetaNamespaceKeyFunc(ns)
	if err != nil {
		uruntime.HandleError(fmt.Errorf("get namespace key %s failed", ns.Name))
		return
	}

	c.nsQueue.Add(key)
}

func (c *NamespacenpController) addWorkspace(newObj interface{}) {
	new := newObj.(*workspacev1alpha1.Workspace)

	klog.V(4).Infof("Add workspace %s", new.Name)

	label := labels.SelectorFromSet(labels.Set{constants.WorkspaceLabelKey: new.Name})
	nsList, err := c.namespaceInformer.Lister().List(label)
	if err != nil {
		klog.Errorf("Error while list namespace by label %s", label.String())
		return
	}

	for _, ns := range nsList {
		c.nsEnqueue(ns)
	}
}

func (c *NamespacenpController) addNamespace(obj interface{}) {
	ns := obj.(*corev1.Namespace)

	klog.Infof("Add namespace %s", ns.Name)

	workspaceName := ns.Labels[constants.WorkspaceLabelKey]
	if workspaceName == "" {
		return
	}

	c.nsEnqueue(ns)
}

func nsNetworkIsolate(ns *corev1.Namespace) bool {
	if ns.Annotations[NamespaceNPAnnotationKey] == NamespaceNPAnnotationEnabled {
		return true
	} else {
		return false
	}
}

func nsNetworkLabel(ns *corev1.Namespace) bool {
	if ns.Annotations[NamespaceLabelKey] == ns.Name {
		return true
	} else {
		return false
	}
}

func (c *NamespacenpController) syncNs(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("not a valid controller key %s, %#v", key, err)
		return err
	}

	ns, err := c.namespaceInformer.Lister().Get(name)
	if err != nil {
		// cluster not found, possibly been deleted
		if errors.IsNotFound(err) {
			klog.V(2).Infof("Namespace %v has been deleted", key)
			return nil
		}

		return err
	}

	workspaceName := ns.Labels[constants.WorkspaceLabelKey]
	if workspaceName == "" {
		return fmt.Errorf("Workspace name should not be empty")
	}
	wksp, err := c.workspaceInformer.Lister().Get(workspaceName)
	if err != nil {
		//Should not be here
		if errors.IsNotFound(err) {
			klog.V(2).Infof("workspace %v has been deleted", workspaceName)
			return nil
		}

		return err
	}

	//Maybe some ns not labeled
	if !nsNetworkLabel(ns) {
		ns.Labels[NamespaceLabelKey] = ns.Name
		_, err := c.client.CoreV1().Namespaces().Update(ns)
		if err != nil {
			klog.Errorf("cannot label namespace %s", ns.Name)
		}
	}

	isworkspace := false
	delete := false
	if nsNetworkIsolate(ns) {
		isworkspace = false
	} else if wksp.Spec.NetworkIsolation {
		isworkspace = true
	} else {
		delete = true
	}

	policy := nsNP(workspaceName, ns.Name, isworkspace)
	if delete {
		c.provider.Delete(c.provider.GetKey(AnnotationNPNAME, ns.Name))
		//delete all namespace np when networkisolate not active
		if c.ksclient.NamespaceNetworkPolicies(ns.Name).DeleteCollection(nil, typev1.ListOptions{}) != nil {
			klog.Errorf("Error when delete all nsnps in namespace %s", ns.Name)
		}
	} else {
		err = c.provider.Set(policy)
		if err != nil {
			klog.Errorf("Error while converting %#v to provider's network policy.", policy)
			return err
		}
	}

	return nil
}

func (c *NamespacenpController) nsWorker() {
	for c.processNsWorkItem() {
	}
}

func (c *NamespacenpController) processNsWorkItem() bool {
	key, quit := c.nsQueue.Get()
	if quit {
		return false
	}
	defer c.nsQueue.Done(key)

	c.syncNs(key.(string))

	return true
}

func (c *NamespacenpController) nsnpEnqueue(obj interface{}) {
	nsnp := obj.(*v1alpha1.NamespaceNetworkPolicy)

	key, err := cache.MetaNamespaceKeyFunc(nsnp)
	if err != nil {
		uruntime.HandleError(fmt.Errorf("get namespace network policy key %s failed", nsnp.Name))
		return
	}

	c.nsnpQueue.Add(key)
}

func (c *NamespacenpController) syncNsNP(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("not a valid controller key %s, %#v", key, err)
		return err
	}

	ns, err := c.namespaceInformer.Lister().Get(namespace)
	if !nsNetworkIsolate(ns) {
		klog.Infof("nsnp %s deleted when namespace isolate is inactive", key)
		c.provider.Delete(c.provider.GetKey(name, namespace))
		return nil
	}

	nsnp, err := c.informer.Lister().NamespaceNetworkPolicies(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Infof("namespacenp %v has been deleted", key)
			c.provider.Delete(c.provider.GetKey(name, namespace))
			return nil
		}

		return err
	}

	np, err := c.convertToK8sNp(nsnp)
	if err != nil {
		klog.Errorf("Error while convert nsnp to k8snp: %s", err)
		return err
	}
	err = c.provider.Set(np)
	if err != nil {
		return err
	}

	return nil
}

func (c *NamespacenpController) nsNPWorker() {
	for c.processNsNPWorkItem() {
	}
}

func (c *NamespacenpController) processNsNPWorkItem() bool {
	key, quit := c.nsnpQueue.Get()
	if quit {
		return false
	}
	defer c.nsnpQueue.Done(key)

	c.syncNsNP(key.(string))

	return true
}

// NewnamespacenpController returns a controller which manages NetworkPolicy objects.
func NewnamespacenpController(
	client kubernetes.Interface,
	ksclient ksnetclient.NetworkV1alpha1Interface,
	nsnpInformer nspolicy.NamespaceNetworkPolicyInformer,
	serviceInformer v1.ServiceInformer,
	workspaceInformer workspace.WorkspaceInformer,
	namespaceInformer v1.NamespaceInformer,
	policyProvider provider.NsNetworkPolicyProvider) *NamespacenpController {

	controller := &NamespacenpController{
		client:                  client,
		ksclient:                ksclient,
		informer:                nsnpInformer,
		informerSynced:          nsnpInformer.Informer().HasSynced,
		serviceInformer:         serviceInformer,
		serviceInformerSynced:   serviceInformer.Informer().HasSynced,
		workspaceInformer:       workspaceInformer,
		workspaceInformerSynced: workspaceInformer.Informer().HasSynced,
		namespaceInformer:       namespaceInformer,
		namespaceInformerSynced: namespaceInformer.Informer().HasSynced,
		provider:                policyProvider,
		nsQueue:                 workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "namespace"),
		nsnpQueue:               workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "namespacenp"),
	}

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.addWorkspace,
		UpdateFunc: func(oldObj, newObj interface{}) {
			controller.addWorkspace(newObj)
		},
	})

	namespaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.addNamespace,
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			controller.addNamespace(newObj)
		},
	})

	// Bind the Calico cache to kubernetes cache with the help of an informer. This way we make sure that
	// whenever the kubernetes cache is updated, changes get reflected in the Calico cache as well.
	nsnpInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			klog.V(4).Infof("Got ADD event for namespace network policy: %#v", obj)
			controller.nsnpEnqueue(obj)
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			klog.V(4).Info("Got UPDATE event for NetworkPolicy.")
			klog.V(4).Infof("Old object: \n%#v\n", oldObj)
			klog.V(4).Infof("New object: \n%#v\n", newObj)
			controller.nsnpEnqueue(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			klog.V(4).Infof("Got DELETE event for NetworkPolicy: %#v", obj)
			controller.nsnpEnqueue(obj)
		},
	}, defaultSleepDuration)

	return controller
}

func (c *NamespacenpController) Start(stopCh <-chan struct{}) error {
	c.Run(defaultThread, defaultSync, stopCh)
	return nil
}

// Run starts the controller.
func (c *NamespacenpController) Run(threadiness int, reconcilerPeriod string, stopCh <-chan struct{}) {
	defer uruntime.HandleCrash()

	defer c.nsQueue.ShutDown()
	defer c.nsnpQueue.ShutDown()

	// Wait until we are in sync with the Kubernetes API before starting the
	// resource cache.
	klog.Info("Waiting to sync with Kubernetes API (NetworkPolicy)")

	if ok := cache.WaitForCacheSync(stopCh, c.informerSynced, c.serviceInformerSynced, c.workspaceInformerSynced, c.namespaceInformerSynced); !ok {
		klog.Errorf("failed to wait for caches to sync")
	}
	klog.Info("Finished syncing with Kubernetes API (NetworkPolicy)")

	// Start a number of worker threads to read from the queue. Each worker
	// will pull keys off the resource cache event queue and sync them to the
	// Calico datastore.
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.nsWorker, time.Second, stopCh)
		go wait.Until(c.nsNPWorker, time.Second, stopCh)
	}

	klog.Info("NetworkPolicy controller is now running")
	<-stopCh
	klog.Info("Stopping NetworkPolicy controller")
}
