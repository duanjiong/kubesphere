package provider

import (
	"context"
	"fmt"
	rcache "github.com/projectcalico/kube-controllers/pkg/cache"
	"github.com/projectcalico/kube-controllers/pkg/converter"
	api "github.com/projectcalico/libcalico-go/lib/apis/v3"
	constants "github.com/projectcalico/libcalico-go/lib/backend/k8s/conversion"
	kdd "github.com/projectcalico/libcalico-go/lib/backend/k8s/conversion"
	client "github.com/projectcalico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/libcalico-go/lib/errors"
	"github.com/projectcalico/libcalico-go/lib/options"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"reflect"
	"strings"
	"time"
)

func (c *policyController) GetKey(name, nsname string) string {
	policyName := fmt.Sprintf(constants.K8sNetworkPolicyNamePrefix + name)
	return fmt.Sprintf("%s/%s", nsname, policyName)
}

// policyController implements the Controller interface for managing Kubernetes network policies
// and syncing them to the Calico datastore as NetworkPolicies.
type policyController struct {
	resourceCache   rcache.ResourceCache
	calicoClient    client.Interface
	ctx             context.Context
	policyConverter converter.Converter
}

// NewPolicyController returns a controller which manages NetworkPolicy objects.
func newPolicyController(ctx context.Context, c client.Interface) *policyController {
	policyConverter := converter.NewPolicyConverter()

	// Function returns map of policyName:policy stored by policy controller
	// in datastore.
	listFunc := func() (map[string]interface{}, error) {
		// Get all policies from datastore
		calicoPolicies, err := c.NetworkPolicies().List(ctx, options.ListOptions{})
		if err != nil {
			return nil, err
		}

		// Filter in only objects that are written by policy controller.
		m := make(map[string]interface{})
		for _, policy := range calicoPolicies.Items {
			if strings.HasPrefix(policy.Name, kdd.K8sNetworkPolicyNamePrefix) {
				// Update the network policy's ObjectMeta so that it simply contains the name and namespace.
				// There is other metadata that we might receive (like resource version) that we don't want to
				// compare in the cache.
				policy.ObjectMeta = metav1.ObjectMeta{Name: policy.Name, Namespace: policy.Namespace}
				k := policyConverter.GetKey(policy)
				m[k] = policy
			}
		}

		klog.V(4).Infof("Found %d policies in Calico datastore:", len(m))
		return m, nil
	}

	cacheArgs := rcache.ResourceCacheArgs{
		ListFunc:   listFunc,
		ObjectType: reflect.TypeOf(api.NetworkPolicy{}),
	}
	ccache := rcache.NewResourceCache(cacheArgs)

	return &policyController{ccache, c, ctx, converter.NewPolicyConverter()}
}

func (c *policyController) Start(stopCh chan struct{}) {
	c.run(5, "5m", stopCh)
}

func (c *policyController) Set(np *netv1.NetworkPolicy) error {
	policy, err := c.policyConverter.Convert(np)
	if err != nil {
		return err
	}

	// Add to cache.
	k := c.policyConverter.GetKey(policy)
	c.resourceCache.Set(k, policy)

	return nil
}

func (c *policyController) Delete(key string) {
	c.resourceCache.Delete(key)
}

// Run starts the controller.
func (c *policyController) run(threadiness int, reconcilerPeriod string, stopCh chan struct{}) {
	defer uruntime.HandleCrash()

	// Let the workers stop when we are done
	workqueue := c.resourceCache.GetQueue()
	defer workqueue.ShutDown()

	// Start the resource cache - this will trigger the queueing of any keys
	// that are out of sync onto the resource cache event queue.
	c.resourceCache.Run(reconcilerPeriod)

	// Start a number of worker threads to read from the queue. Each worker
	// will pull keys off the resource cache event queue and sync them to the
	// Calico datastore.
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	klog.Info("NetworkPolicy controller is now running")

	<-stopCh
	klog.Info("Stopping NetworkPolicy controller")
}

func (c *policyController) runWorker() {
	for c.processNextItem() {
	}
}

// processNextItem waits for an event on the output queue from the resource cache and syncs
// any received keys to the datastore.
func (c *policyController) processNextItem() bool {
	// Wait until there is a new item in the work queue.
	workqueue := c.resourceCache.GetQueue()
	key, quit := workqueue.Get()
	if quit {
		return false
	}

	// Sync the object to the Calico datastore.
	if err := c.syncToDatastore(key.(string)); err != nil {
		c.handleErr(err, key.(string))
	}

	// Indicate that we're done processing this key, allowing for safe parallel processing such that
	// two objects with the same key are never processed in parallel.
	workqueue.Done(key)
	return true
}

// syncToDatastore syncs the given update to the Calico datastore. The provided key can be used to
// find the corresponding resource within the resource cache. If the resource for the provided key
// exists in the cache, then the value should be written to the datastore. If it does not exist
// in the cache, then it should be deleted from the datastore.
func (c *policyController) syncToDatastore(key string) error {
	// Check if it exists in the controller's cache.
	obj, exists := c.resourceCache.Get(key)
	if !exists {
		// The object no longer exists - delete from the datastore.
		klog.Infof("Deleting NetworkPolicy %s from Calico datastore", key)
		ns, name := converter.NewPolicyConverter().DeleteArgsFromKey(key)
		_, err := c.calicoClient.NetworkPolicies().Delete(c.ctx, ns, name, options.DeleteOptions{})
		if _, ok := err.(errors.ErrorResourceDoesNotExist); !ok {
			// We hit an error other than "does not exist".
			return err
		}
		return nil
	} else {
		// The object exists - update the datastore to reflect.
		klog.Infof("Create/Update NetworkPolicy %s in Calico datastore", key)
		p := obj.(api.NetworkPolicy)

		// Lookup to see if this object already exists in the datastore.
		gp, err := c.calicoClient.NetworkPolicies().Get(c.ctx, p.Namespace, p.Name, options.GetOptions{})
		if err != nil {
			if _, ok := err.(errors.ErrorResourceDoesNotExist); !ok {
				klog.Warningf("Failed to get network policy %s from datastore", key)
				return err
			}

			// Doesn't exist - create it.
			_, err := c.calicoClient.NetworkPolicies().Create(c.ctx, &p, options.SetOptions{})
			if err != nil {
				klog.Warningf("Failed to create network policy %s", key)
				return err
			}
			klog.Infof("Successfully created network policy %s", key)
			return nil
		}

		// The policy already exists, update it and write it back to the datastore.
		gp.Spec = p.Spec
		_, err = c.calicoClient.NetworkPolicies().Update(c.ctx, gp, options.SetOptions{})
		if err != nil {
			klog.Warningf("Failed to update network policy %s", key)
			return err
		}
		klog.Infof("Successfully updated network policy %s", key)
		return nil
	}
}

// handleErr handles errors which occur while processing a key received from the resource cache.
// For a given error, we will re-queue the key in order to retry the datastore sync up to 5 times,
// at which point the update is dropped.
func (c *policyController) handleErr(err error, key string) {
	workqueue := c.resourceCache.GetQueue()
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		workqueue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if workqueue.NumRequeues(key) < 5 {
		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		klog.Errorf("Error syncing Policy %v: %v", key, err)
		workqueue.AddRateLimited(key)
		return
	}
	workqueue.Forget(key)

	// Report to an external entity that, even after several retries, we could not successfully process this key
	uruntime.HandleError(err)
	klog.Errorf("Dropping Policy %q out of the queue: %v", key, err)
}

func NewCalicoNetworkProvider() (NsNetworkPolicyProvider, error) {
	// Get Calico client
	calicoClient, err := client.NewFromEnv()
	if err != nil {
		return nil, err
	}

	return newPolicyController(context.Background(), calicoClient), nil
}
