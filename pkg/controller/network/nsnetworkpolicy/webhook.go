package nsnetworkpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-v1-service,mutating=false,failurePolicy=fail,groups="",resources=service,verbs=create;update,versions=v1

// serviceValidator validates service
type ServiceValidator struct {
	decoder *admission.Decoder
}

// Service must hash label, becasue nsnp will use it
func (v *ServiceValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	service := &corev1.Service{}

	err := v.decoder.Decode(req, service)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if service.Spec.Selector != nil {
		return admission.Denied(fmt.Sprintf("missing label"))
	}

	return admission.Allowed("")
}

func (a *ServiceValidator) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}

// +kubebuilder:webhook:path=/mutate-v1-namespace,mutating=true,failurePolicy=fail,groups="",resources=namespace,verbs=create;update,versions=v1

type NamespaceAnnotator struct {
	decoder *admission.Decoder
}

func (a *NamespaceAnnotator) Handle(ctx context.Context, req admission.Request) admission.Response {
	ns := &corev1.Namespace{}

	err := a.decoder.Decode(req, ns)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels[NamespaceLabelKey] = ns.Name

	marshaledNS, err := json.Marshal(ns)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledNS)
}

func (a *NamespaceAnnotator) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}

func RegisterWebhooks(mgr manager.Manager) {
	webhookServer := mgr.GetWebhookServer()
	webhookServer.Register("/mutate-v1-namespace", &webhook.Admission{Handler: &NamespaceAnnotator{}})
	webhookServer.Register("/validate-v1-service", &webhook.Admission{Handler: &ServiceValidator{}})
}
