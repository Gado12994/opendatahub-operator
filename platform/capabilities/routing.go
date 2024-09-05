package capabilities

import (
	"context"
	"fmt"
	"path"

	"github.com/opendatahub-io/odh-platform/pkg/platform"
	"github.com/opendatahub-io/odh-platform/pkg/routing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	featurev1 "github.com/opendatahub-io/opendatahub-operator/v2/apis/features/v1"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/feature"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/feature/manifest"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/feature/servicemesh"
)

func NewRouting(spec RoutingSpec, available bool) *RoutingCapability {
	return &RoutingCapability{
		available:   available,
		routingSpec: spec,
	}
}

// Routing is component-facing interface allowing ODH components to enroll to platform's routing capability.
type Routing interface {
	IsAvailable() bool
	// Expose defines which resources should be watched and updated
	// for the routing capability for a given component.
	Expose(targets ...platform.RoutingTarget)
}

type RoutingCapability struct {
	available      bool
	routingSpec    RoutingSpec
	routingTargets []platform.RoutingTarget
}

func (r *RoutingCapability) IngressConfig() routing.IngressConfig {
	return routing.IngressConfig{
		IngressSelectorLabel: r.routingSpec.IngressGateway.LabelSelectorKey,
		IngressSelectorValue: r.routingSpec.IngressGateway.LabelSelectorValue,
		IngressService:       r.routingSpec.IngressGateway.Name,
		GatewayNamespace:     r.routingSpec.IngressGateway.Namespace,
	}
}

func (r *RoutingCapability) RoutingTargets() []platform.RoutingTarget {
	return r.routingTargets
}

// Component registration API.
var _ Routing = (*RoutingCapability)(nil)

func (r *RoutingCapability) Expose(targets ...platform.RoutingTarget) {
	r.routingTargets = append(r.routingTargets, targets...)
}

func (r *RoutingCapability) IsAvailable() bool {
	return r.available
}

// Platform configuration managed by the operator.
var _ Reconciler = (*RoutingCapability)(nil)

func (r *RoutingCapability) IsRequired() bool {
	return len(r.routingTargets) > 0
}

// Reconcile ensures routing capability and component-specific configuration is wired when needed.
func (r *RoutingCapability) Reconcile(ctx context.Context, cli client.Client, owner metav1.Object) error {
	const roleName = "platform-routing-resources-watcher"

	withOwnerRef, err := cluster.AsOwnerRef(owner)
	if err != nil {
		return fmt.Errorf("failed to define meta options while reconciling routing capability: %w", err)
	}

	objectReferences := make([]platform.ResourceReference, len(r.routingTargets))
	for i, ref := range r.routingTargets {
		objectReferences[i] = ref.ResourceReference
	}

	if errRoleCreate := CreateOrUpdatePlatformRBAC(ctx, cli, roleName, objectReferences, withOwnerRef); errRoleCreate != nil {
		return fmt.Errorf("failed to create role bindings for platform routing: %w", errRoleCreate)
	}

	routingFeatures := feature.NewFeaturesHandler(
		r.routingSpec.IngressGateway.Namespace,
		featurev1.Source{Type: featurev1.PlatformCapabilityType, Name: "routing"},
		r.defineRoutingFeatures(owner),
	)

	return routingFeatures.Apply(ctx)
}

func (r *RoutingCapability) defineRoutingFeatures(owner metav1.Object) feature.FeaturesProvider {
	return func(registry feature.FeaturesRegistry) error {
		required := func(_ context.Context, _ *feature.Feature) (bool, error) {
			return len(r.routingTargets) > 0, nil
		}

		return registry.Add(
			feature.Define("mesh-ingress-ns-creation").
				Manifests(
					manifest.Location(Templates.Location).
						Include(
							path.Join(Templates.ServiceMeshIngressDir, "servicemeshmember.tmpl.yaml"),
						),
				).
				Managed().
				OwnedBy(owner).
				EnabledWhen(required).
				WithData(r.routingSpec).
				PreConditions(
					servicemesh.EnsureServiceMeshOperatorInstalled,
					feature.CreateNamespaceIfNotExists(r.routingSpec.IngressGateway.Namespace),
				).
				PostConditions(
					servicemesh.WaitForServiceMeshMember(r.routingSpec.IngressGateway.Namespace),
				),
			feature.Define("mesh-ingress-creation").
				Manifests(
					manifest.Location(Templates.Location).
						Include(
							path.Join(Templates.ServiceMeshIngressDir, "service.tmpl.yaml"),
							path.Join(Templates.ServiceMeshIngressDir, "role.tmpl.yaml"),
							path.Join(Templates.ServiceMeshIngressDir, "rolebinding.tmpl.yaml"),
							path.Join(Templates.ServiceMeshIngressDir, "deployment.tmpl.yaml"),
							path.Join(Templates.ServiceMeshIngressDir, "gateway.tmpl.yaml"),
							path.Join(Templates.ServiceMeshIngressDir, "networkpolicy.tmpl.yaml"),
						),
				).
				Managed().
				OwnedBy(owner).
				EnabledWhen(required).
				WithData(r.routingSpec).
				PreConditions(
					servicemesh.EnsureServiceMeshOperatorInstalled,
					feature.CreateNamespaceIfNotExists(r.routingSpec.IngressGateway.Namespace),
				).
				PostConditions(
					feature.WaitForPodsToBeReady(r.routingSpec.IngressGateway.Namespace),
				),
		)
	}
}