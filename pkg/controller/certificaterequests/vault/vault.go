/*
Copyright 2019 The Jetstack cert-manager contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vault

import (
	"context"
	"fmt"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/record"

	apiutil "github.com/jetstack/cert-manager/pkg/api/util"
	"github.com/jetstack/cert-manager/pkg/apis/certmanager/v1alpha1"
	controllerpkg "github.com/jetstack/cert-manager/pkg/controller"
	"github.com/jetstack/cert-manager/pkg/controller/certificaterequests"
	crutil "github.com/jetstack/cert-manager/pkg/controller/certificaterequests/util"
	vaultinternal "github.com/jetstack/cert-manager/pkg/internal/vault"
	"github.com/jetstack/cert-manager/pkg/issuer"
	logf "github.com/jetstack/cert-manager/pkg/logs"
	"github.com/jetstack/cert-manager/pkg/util/pki"
)

const (
	CRControllerName = "certificaterequests-issuer-vault"
)

type Vault struct {
	// used to record Events about resources to the API
	recorder      record.EventRecorder
	secretsLister corelisters.SecretLister
	helper        issuer.Helper
}

func init() {
	// create certificate request controller for ca issuer
	controllerpkg.Register(CRControllerName, func(ctx *controllerpkg.Context) (controllerpkg.Interface, error) {
		vault := NewVault(ctx)

		controller := certificaterequests.New(apiutil.IssuerVault, vault)

		c, err := controllerpkg.New(ctx, CRControllerName, controller)
		if err != nil {
			return nil, err
		}

		return c.Run, nil
	})
}

func NewVault(ctx *controllerpkg.Context) *Vault {
	return &Vault{
		recorder:      ctx.Recorder,
		secretsLister: ctx.KubeSharedInformerFactory.Core().V1().Secrets().Lister(),
		helper: issuer.NewHelper(
			ctx.SharedInformerFactory.Certmanager().V1alpha1().Issuers().Lister(),
			ctx.SharedInformerFactory.Certmanager().V1alpha1().ClusterIssuers().Lister(),
		),
	}
}

func (v *Vault) Sign(ctx context.Context, cr *v1alpha1.CertificateRequest) (*issuer.IssueResponse, error) {
	log := logf.FromContext(ctx, "sign")

	reporter := crutil.NewReporter(log, cr, v.recorder)

	issuerObj, err := v.helper.GetGenericIssuer(cr.Spec.IssuerRef, cr.Namespace)
	if err != nil {
		log = log.WithValues(
			logf.RelatedResourceNameKey, cr.Spec.IssuerRef.Name,
			logf.RelatedResourceKindKey, cr.Spec.IssuerRef.Kind,
		)

		if k8sErrors.IsNotFound(err) {
			reporter.WithLog(log).Pending(err, v1alpha1.CertificateRequestReasonPending,
				fmt.Sprintf("Referenced %s not found", apiutil.IssuerKind(cr.Spec.IssuerRef)))
			return nil, nil
		}

		reporter.WithLog(log).Pending(err, v1alpha1.CertificateRequestReasonPending,
			fmt.Sprintf("Referenced %s not found", apiutil.IssuerKind(cr.Spec.IssuerRef)))
		return nil, err
	}

	_, err = pki.DecodeX509CertificateRequestBytes(cr.Spec.CSRPEM)
	if err != nil {
		reporter.Failed(err, "ErrorParsingCSR",
			fmt.Sprintf("Failed to decode CSR in spec: %s", err))
		return nil, nil
	}

	client, err := vaultinternal.New(cr.Namespace, v.secretsLister, issuerObj)
	if err != nil {
		reporter.Pending(err, "ErrorVaultInit",
			fmt.Sprintf("Failed to initialise vault client for signing: %s", err))
		return nil, nil
	}

	certPem, caPem, err := client.Sign(cr.Spec.CSRPEM, cr.Spec.Duration.Duration)
	if err != nil {
		reporter.Failed(err, "ErrorSigning",
			fmt.Sprintf("Vault failed to sign certificate: %s", err))
		return nil, nil
	}

	log.Info("certificate issued")

	return &issuer.IssueResponse{
		Certificate: certPem,
		CA:          caPem,
	}, nil
}
