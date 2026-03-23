// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !plan9

package main

import (
	"context"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"tailscale.com/internal/client/tailscale"
	tsapi "tailscale.com/k8s-operator/apis/v1alpha1"
)

func TestClientForTailnet(t *testing.T) {
	tests := []struct {
		name       string
		secretData map[string][]byte
		wantToken  string
	}{
		{
			name: "static_oauth",
			secretData: map[string][]byte{
				"client_id":     []byte("test-client-id"),
				"client_secret": []byte("test-client-secret"),
			},
			wantToken: "Bearer " + testToken("/api/v2/oauth/token", "test-client-id", "test-client-secret", ""),
		},
		{
			name: "wif_jwt",
			secretData: map[string][]byte{
				"client_id": []byte("test-client-id"),
				"jwt":       []byte("test-jwt-token"),
			},
			wantToken: "Bearer " + testToken("/api/v2/oauth/token-exchange", "test-client-id", "", "test-jwt-token"),
		},
		{
			name: "jwt_preferred_over_client_secret",
			secretData: map[string][]byte{
				"client_id":     []byte("test-client-id"),
				"client_secret": []byte("test-client-secret"),
				"jwt":           []byte("test-jwt-token"),
			},
			wantToken: "Bearer " + testToken("/api/v2/oauth/token-exchange", "test-client-id", "", "test-jwt-token"),
		},
		{
			name: "empty_jwt_falls_back_to_static",
			secretData: map[string][]byte{
				"client_id":     []byte("test-client-id"),
				"client_secret": []byte("test-client-secret"),
				"jwt":           []byte(""),
			},
			wantToken: "Bearer " + testToken("/api/v2/oauth/token", "test-client-id", "test-client-secret", ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testAPI(t, 3600)

			tn := &tsapi.Tailnet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-tailnet",
				},
				Spec: tsapi.TailnetSpec{
					LoginURL: srv.URL,
					Credentials: tsapi.TailnetCredentials{
						SecretName: "test-secret",
					},
				},
				Status: tsapi.TailnetStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(tsapi.TailnetReady),
							Status: metav1.ConditionTrue,
						},
					},
				},
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: tt.secretData,
			}

			fc := fake.NewClientBuilder().
				WithScheme(tsapi.GlobalScheme).
				WithObjects(tn, secret).
				WithStatusSubresource(tn).
				Build()

			tsClient, baseURL, err := clientForTailnet(context.Background(), fc, "default", "test-tailnet")
			if err != nil {
				t.Fatalf("clientForTailnet: %v", err)
			}
			if baseURL != srv.URL {
				t.Errorf("baseURL = %q, want %q", baseURL, srv.URL)
			}

			cl, ok := tsClient.(*tailscale.Client)
			if !ok {
				t.Fatalf("expected *tailscale.Client, got %T", tsClient)
			}

			resp, err := cl.HTTPClient.Get(srv.URL)
			if err != nil {
				t.Fatalf("HTTP GET: %v", err)
			}
			defer resp.Body.Close()

			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading response: %v", err)
			}
			if string(got) != tt.wantToken {
				t.Errorf("got %q, want %q", got, tt.wantToken)
			}
		})
	}
}

func TestClientForTailnetErrors(t *testing.T) {
	tests := []struct {
		name        string
		tailnetName string
		wantErr     string
		setup       func(*fake.ClientBuilder)
	}{
		{
			name:        "tailnet_not_found",
			tailnetName: "missing",
			wantErr:     `failed to get tailnet "missing"`,
			setup:       func(b *fake.ClientBuilder) {},
		},
		{
			name:        "tailnet_not_ready",
			tailnetName: "not-ready",
			wantErr:     `tailnet "not-ready" is not ready`,
			setup: func(b *fake.ClientBuilder) {
				tn := &tsapi.Tailnet{
					ObjectMeta: metav1.ObjectMeta{Name: "not-ready"},
					Spec: tsapi.TailnetSpec{
						Credentials: tsapi.TailnetCredentials{SecretName: "test-secret"},
					},
				}
				b.WithObjects(tn).WithStatusSubresource(tn)
			},
		},
		{
			name:        "secret_not_found",
			tailnetName: "bad-secret",
			wantErr:     `failed to get Secret "missing-secret"`,
			setup: func(b *fake.ClientBuilder) {
				tn := &tsapi.Tailnet{
					ObjectMeta: metav1.ObjectMeta{Name: "bad-secret"},
					Spec: tsapi.TailnetSpec{
						Credentials: tsapi.TailnetCredentials{SecretName: "missing-secret"},
					},
					Status: tsapi.TailnetStatus{
						Conditions: []metav1.Condition{
							{Type: string(tsapi.TailnetReady), Status: metav1.ConditionTrue},
						},
					},
				}
				b.WithObjects(tn).WithStatusSubresource(tn)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(tsapi.GlobalScheme)
			tt.setup(b)
			fc := b.Build()

			_, _, err := clientForTailnet(context.Background(), fc, "default", tt.tailnetName)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
