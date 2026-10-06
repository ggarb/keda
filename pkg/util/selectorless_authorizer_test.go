/*
Copyright 2026 The KEDA Authors

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

package util

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/plugin/pkg/authorizer/webhook"
	webhookmetrics "k8s.io/apiserver/plugin/pkg/authorizer/webhook/metrics"
	"k8s.io/client-go/rest"
)

func TestSelectorlessAuthorizerDropsSelectors(t *testing.T) {
	req, err := labels.NewRequirement("scaledobject.keda.sh/name", selection.Equals, []string{"so-1"})
	assert.NoError(t, err)
	attrs := authorizer.AttributesRecord{
		User:                      &user.DefaultInfo{Name: "system:serviceaccount:kube-system:horizontal-pod-autoscaler"},
		Verb:                      "list",
		Namespace:                 "default",
		APIGroup:                  "external.metrics.k8s.io",
		Resource:                  "s0-prometheus",
		ResourceRequest:           true,
		LabelSelectorRequirements: labels.Requirements{*req},
		FieldSelectorRequirements: fields.Requirements{{Operator: selection.Equals, Field: "metadata.name", Value: "x"}},
	}

	var seen authorizer.Attributes
	inner := authorizer.AuthorizerFunc(func(_ context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		seen = a
		return authorizer.DecisionAllow, "ok", nil
	})

	decision, reason, err := selectorlessAuthorizer{inner}.Authorize(context.Background(), attrs)
	assert.NoError(t, err)
	assert.Equal(t, authorizer.DecisionAllow, decision)
	assert.Equal(t, "ok", reason)

	ls, lerr := seen.GetLabelSelector()
	assert.NoError(t, lerr)
	assert.Empty(t, ls)
	fs, ferr := seen.GetFieldSelector()
	assert.NoError(t, ferr)
	assert.Empty(t, fs)
	// everything else reaches the inner authorizer unchanged
	assert.Equal(t, "list", seen.GetVerb())
	assert.Equal(t, "default", seen.GetNamespace())
	assert.Equal(t, "s0-prometheus", seen.GetResource())
	assert.Equal(t, attrs.User.GetName(), seen.GetUser().GetName())
}

func TestSelectorlessAuthorizerPassesThroughDenyAndError(t *testing.T) {
	inner := authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
		return authorizer.DecisionDeny, "forbidden", errors.New("boom")
	})

	decision, reason, err := selectorlessAuthorizer{inner}.Authorize(context.Background(), authorizer.AttributesRecord{Verb: "list"})
	assert.Equal(t, authorizer.DecisionDeny, decision)
	assert.Equal(t, "forbidden", reason)
	assert.EqualError(t, err, "boom")
}

// With the real webhook authorizer (cache on, selectors in the review), HPAs
// that differ only in their label selector share one cached
// SubjectAccessReview once the selectors are dropped, and cost one each
// otherwise.
func TestSelectorlessAuthorizerSharesCachedReview(t *testing.T) {
	var reviews atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reviews.Add(1)
		var sar authorizationv1.SubjectAccessReview
		_ = json.NewDecoder(r.Body).Decode(&sar)
		sar.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: true}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sar)
	}))
	defer srv.Close()

	newWebhook := func() authorizer.Authorizer {
		a, err := webhook.New(&rest.Config{Host: srv.URL}, "v1", time.Minute, time.Minute,
			wait.Backoff{Steps: 1}, authorizer.DecisionDeny, nil, "test", webhookmetrics.NoopAuthorizerMetrics{}, nil)
		assert.NoError(t, err)
		return a
	}
	hpaRequest := func(so string) authorizer.Attributes {
		req, err := labels.NewRequirement("scaledobject.keda.sh/name", selection.Equals, []string{so})
		assert.NoError(t, err)
		return authorizer.AttributesRecord{
			User:                      &user.DefaultInfo{Name: "system:serviceaccount:kube-system:horizontal-pod-autoscaler"},
			Verb:                      "list",
			Namespace:                 "default",
			APIGroup:                  "external.metrics.k8s.io",
			Resource:                  "s0-prometheus",
			ResourceRequest:           true,
			LabelSelectorRequirements: labels.Requirements{*req},
		}
	}

	for _, tc := range []struct {
		name        string
		authz       authorizer.Authorizer
		wantReviews int32
	}{
		{name: "with selectors", authz: newWebhook(), wantReviews: 3},
		{name: "selectorless", authz: selectorlessAuthorizer{newWebhook()}, wantReviews: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviews.Store(0)
			for _, so := range []string{"so-1", "so-2", "so-3"} {
				decision, _, err := tc.authz.Authorize(context.Background(), hpaRequest(so))
				assert.NoError(t, err)
				assert.Equal(t, authorizer.DecisionAllow, decision)
			}
			assert.Equal(t, tc.wantReviews, reviews.Load())
		})
	}
}
