//
// Copyright (c) 2020-2020 Red Hat, Inc.
// This program and the accompanying materials are made
// available under the terms of the Eclipse Public License 2.0
// which is available at https://www.eclipse.org/legal/epl-2.0/
//
// SPDX-License-Identifier: EPL-2.0
//
// Contributors:
//   Red Hat, Inc. - initial API and implementation
//
package identity_provider

import (
	"context"
	"fmt"
	"strings"

	orgv1 "github.com/eclipse/che-operator/pkg/apis/org/v1"
	"github.com/eclipse/che-operator/pkg/deploy"
	"github.com/eclipse/che-operator/pkg/deploy/expose"

	"github.com/eclipse/che-operator/pkg/util"
	"github.com/google/go-cmp/cmp/cmpopts"
	oauth "github.com/openshift/api/oauth/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

const (
	IdentityProviderServiceName    = "keycloak"
	IdentityProviderExposureName   = "keycloak"
	IdentityProviderDeploymentName = "keycloak"
)

var (
	oAuthClientDiffOpts = cmpopts.IgnoreFields(oauth.OAuthClient{}, "TypeMeta", "ObjectMeta")
	syncItems           = []func(*deploy.DeployContext) (bool, error){
		syncService,
		syncExposure,
		syncDeployment,
		syncKeycloakResources,
		syncOpenShiftIdentityProvider,
		syncGitHubFederatedIdentity,
	}
)

// SyncIdentityProviderToCluster instantiates the identity provider (Keycloak) in the cluster. Returns true if
// the provisioning is complete, false if requeue of the reconcile request is needed.
func SyncIdentityProviderToCluster(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster
	if cr.Spec.Auth.ExternalIdentityProvider {
		return true, nil
	}

	cheMultiUser := deploy.GetCheMultiUser(cr)
	if cheMultiUser == "false" {
		if util.K8sclient.IsDeploymentExists("keycloak", cr.Namespace) {
			util.K8sclient.DeleteDeployment("keycloak", cr.Namespace)
		}
		return true, nil
	}

	for _, syncItem := range syncItems {
		provisioned, err := syncItem(deployContext)
		if !util.IsTestMode() {
			if !provisioned {
				return provisioned, err
			}
		}
	}

	return true, nil
}

func syncService(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster
	labels := deploy.GetLabels(cr, IdentityProviderServiceName)

	serviceStatus := deploy.SyncServiceToCluster(
		deployContext,
		IdentityProviderServiceName,
		[]string{"http"},
		[]int32{8080},
		labels)

	return serviceStatus.Continue, serviceStatus.Err
}

func syncExposure(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster

	protocol := (map[bool]string{
		true:  "https",
		false: "http"})[cr.Spec.Server.TlsSupport]
	additionalLabels := (map[bool]string{
		true:  cr.Spec.Auth.IdentityProviderRoute.Labels,
		false: cr.Spec.Auth.IdentityProviderIngress.Labels})[util.IsOpenShift]

	endpoint, done, err := expose.Expose(
		deployContext,
		cr.Spec.Server.CheHost,
		IdentityProviderExposureName,
		additionalLabels)
	if !done {
		return false, err
	}

	keycloakURL := protocol + "://" + endpoint
	deployContext.InternalService.KeycloakHost = fmt.Sprintf("%s://%s.%s.svc:%d", "http", "keycloak", cr.Namespace, 8080)

	if cr.Spec.Auth.IdentityProviderURL != keycloakURL {
		cr.Spec.Auth.IdentityProviderURL = keycloakURL
		if err := deploy.UpdateCheCRSpec(deployContext, "Keycloak URL", keycloakURL); err != nil {
			return false, err
		}

		cr.Status.KeycloakURL = keycloakURL
		if err := deploy.UpdateCheCRStatus(deployContext, "Keycloak URL", keycloakURL); err != nil {
			return false, err
		}
	}

	return true, nil
}

func syncKeycloakResources(deployContext *deploy.DeployContext) (bool, error) {
	if !util.IsTestMode() {
		cr := deployContext.CheCluster
		if !cr.Status.KeycloakProvisoned {
			if err := ProvisionKeycloakResources(deployContext); err != nil {
				return false, err
			}

			for {
				cr.Status.KeycloakProvisoned = true
				if err := deploy.UpdateCheCRStatus(deployContext, "status: provisioned with Keycloak", "true"); err != nil &&
					errors.IsConflict(err) {

					reload(deployContext)
					continue
				}
				break
			}
		}
	}

	return true, nil
}

func syncDeployment(deployContext *deploy.DeployContext) (bool, error) {
	deploymentStatus := SyncKeycloakDeploymentToCluster(deployContext)
	return deploymentStatus.Continue, deploymentStatus.Err
}

func syncOpenShiftIdentityProvider(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster
	if util.IsOpenShift && util.IsOAuthEnabled(cr) {
		return SyncOpenShiftIdentityProviderItems(deployContext)
	}
	return true, nil
}

func SyncOpenShiftIdentityProviderItems(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster

	oAuthClientName := cr.Spec.Auth.OAuthClientName
	if len(oAuthClientName) < 1 {
		oAuthClientName = cr.Name + "-openshift-identity-provider-" + strings.ToLower(util.GeneratePasswd(6))
		cr.Spec.Auth.OAuthClientName = oAuthClientName
		if err := deploy.UpdateCheCRSpec(deployContext, "oAuthClient name", oAuthClientName); err != nil {
			return false, err
		}
	}

	oauthSecret := cr.Spec.Auth.OAuthSecret
	if len(oauthSecret) < 1 {
		oauthSecret = util.GeneratePasswd(12)
		cr.Spec.Auth.OAuthSecret = oauthSecret
		if err := deploy.UpdateCheCRSpec(deployContext, "oAuth secret name", oauthSecret); err != nil {
			return false, err
		}
	}

	keycloakURL := cr.Spec.Auth.IdentityProviderURL
	cheFlavor := deploy.DefaultCheFlavor(cr)
	keycloakRealm := util.GetValue(cr.Spec.Auth.IdentityProviderRealm, cheFlavor)
	oAuthClient := deploy.NewOAuthClient(oAuthClientName, oauthSecret, keycloakURL, keycloakRealm, util.IsOpenShift4)
	provisioned, err := deploy.Sync(deployContext, oAuthClient, oAuthClientDiffOpts)
	if !provisioned {
		return false, err
	}

	if !util.IsTestMode() {
		if !cr.Status.OpenShiftoAuthProvisioned {
			// note that this uses the instance.Spec.Auth.IdentityProviderRealm and instance.Spec.Auth.IdentityProviderClientId.
			// because we're not doing much of a change detection on those fields, we can't react on them changing here.
			_, err := util.K8sclient.ExecIntoPod(
				cr,
				IdentityProviderDeploymentName,
				func(cr *orgv1.CheCluster) (string, error) {
					return GetOpenShiftIdentityProviderProvisionCommand(cr, oAuthClientName, oauthSecret)
				},
				"Create OpenShift identity provider")
			if err != nil {
				return false, err
			}

			for {
				cr.Status.OpenShiftoAuthProvisioned = true
				if err := deploy.UpdateCheCRStatus(deployContext, "status: provisioned with OpenShift identity provider", "true"); err != nil &&
					errors.IsConflict(err) {

					reload(deployContext)
					continue
				}
				break
			}
		}
	}
	return true, nil
}

func syncGitHubFederatedIdentity(deployContext *deploy.DeployContext) (bool, error) {
	cr := deployContext.CheCluster
	if cr.Spec.Auth.FederatedIdentities.GitHub.Enable {
		if !cr.Status.GitHubFederatedIdentityProvisioned {
			_, err := util.K8sclient.ExecIntoPod(
				cr,
				IdentityProviderDeploymentName,
				func(cr *orgv1.CheCluster) (string, error) {
					return GetGitHubIdentityProviderProvisionCommand(deployContext)
				},
				"Create GitHub federated identity")
			if err != nil {
				return false, err
			}

			cr.Status.GitHubFederatedIdentityProvisioned = true
			if err := deploy.UpdateCheCRStatus(deployContext, "status: GitHub federated identity provisioned", "true"); err != nil {
				return false, err
			}
		}
	} else {
		if cr.Status.GitHubFederatedIdentityProvisioned {
			_, err := util.K8sclient.ExecIntoPod(
				cr,
				IdentityProviderDeploymentName,
				func(cr *orgv1.CheCluster) (string, error) {
					return GetDeleteIdentityProviderCommand(cr, "github")
				},
				"Delete GitHub federated identity")
			if err != nil {
				return false, err
			}

			cr.Status.GitHubFederatedIdentityProvisioned = false
			if err := deploy.UpdateCheCRStatus(deployContext, "status: GitHub federated identity provisioned", "false"); err != nil {
				return false, err
			}
		}
	}

	return true, nil
}

func reload(deployContext *deploy.DeployContext) error {
	return deployContext.ClusterAPI.Client.Get(
		context.TODO(),
		types.NamespacedName{Name: deployContext.CheCluster.Name, Namespace: deployContext.CheCluster.Namespace},
		deployContext.CheCluster)
}
