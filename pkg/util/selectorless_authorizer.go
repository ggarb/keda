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

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// NewSelectorlessAuthorizer wraps inner so that it authorizes requests
// without their label and field selectors. Every HPA's external-metrics request carries its own selector
// (scaledobject.keda.sh/name=<so>), and the delegated authorizer puts the
// selector into its SubjectAccessReview and its cache key, so each HPA misses
// the cache and costs a SubjectAccessReview. Those go through a client capped
// at 200 QPS, which then caps the whole cluster at ~200 metric reads/s per
// metrics-server pod. RBAC ignores selectors, so dropping them leaves the
// decision unchanged and lets one cached review serve every HPA.
func NewSelectorlessAuthorizer(inner authorizer.Authorizer) authorizer.Authorizer {
	return selectorlessAuthorizer{inner}
}

type selectorlessAuthorizer struct {
	authorizer.Authorizer
}

func (a selectorlessAuthorizer) Authorize(ctx context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
	return a.Authorizer.Authorize(ctx, selectorlessAttributes{attrs})
}

type selectorlessAttributes struct {
	authorizer.Attributes
}

func (selectorlessAttributes) GetFieldSelector() (fields.Requirements, error) { return nil, nil }

func (selectorlessAttributes) GetLabelSelector() (labels.Requirements, error) { return nil, nil }
