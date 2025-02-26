/*
Copyright 2018 Hetzner Cloud GmbH.

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

package hcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syself/hetzner-cloud-controller-manager/internal/annotation"
	"github.com/syself/hetzner-cloud-controller-manager/internal/credentials"
	"github.com/syself/hetzner-cloud-controller-manager/internal/hcops"
	hrobot "github.com/syself/hrobot-go"
	"github.com/syself/hrobot-go/models"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type testEnv struct {
	Server      *httptest.Server
	Mux         *http.ServeMux
	Client      *hcloud.Client
	RobotClient hrobot.RobotClient
}

func (env *testEnv) Teardown() {
	env.Server.Close()
	env.Server = nil
	env.Mux = nil
	env.Client = nil
	env.RobotClient = nil
}

func newTestEnv() testEnv {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	client := hcloud.NewClient(
		hcloud.WithEndpoint(server.URL),
		hcloud.WithToken("jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jNZXCeTYQ4uArypFM3nh75"),
		hcloud.WithBackoffFunc(func(_ int) time.Duration { return 0 }),
		// hcloud.WithDebugWriter(os.Stdout),
	)
	robotClient := hrobot.NewBasicAuthClient("", "")
	robotClient.SetBaseURL(server.URL + "/robot")
	return testEnv{
		Server:      server,
		Mux:         mux,
		Client:      client,
		RobotClient: robotClient,
	}
}

func TestNewCloud(t *testing.T) {
	env := newTestEnv()
	defer env.Teardown()

	resetEnv := Setenv(t,
		"HCLOUD_ENDPOINT", env.Server.URL,
		"HCLOUD_TOKEN", "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jN_NOT_VALID_dzhepnahq",
		"HCLOUD_METRICS_ENABLED", "false",
	)
	defer resetEnv()
	env.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(
			schema.ServerListResponse{
				Servers: []schema.Server{},
			},
		)
	})
	var config bytes.Buffer
	_, err := newCloud(&config)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestNewCloudWrongTokenSize(t *testing.T) {
	resetEnv := Setenv(t,
		"HCLOUD_TOKEN", "0123456789abcdef",
		"HCLOUD_METRICS_ENABLED", "false",
	)
	defer resetEnv()

	var config bytes.Buffer
	_, err := newCloud(&config)
	if err == nil || err.Error() != "hcloud/newCloud: entered token is invalid (must be exactly 64 characters long)" {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestNewCloudConnectionNotPossible(t *testing.T) {
	resetEnv := Setenv(t,
		"HCLOUD_ENDPOINT", "http://127.0.0.1:4711/v1",
		"HCLOUD_TOKEN", "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jN_NOT_VALID_dzhepnahq",
		"HCLOUD_METRICS_ENABLED", "false",
	)
	defer resetEnv()

	_, err := newCloud(&bytes.Buffer{})
	assert.EqualError(t, err,
		`hcloud/newCloud: Get "http://127.0.0.1:4711/v1/servers?": dial tcp 127.0.0.1:4711: connect: connection refused`)
}

func TestNewCloudInvalidToken(t *testing.T) {
	env := newTestEnv()
	defer env.Teardown()

	resetEnv := Setenv(t,
		"HCLOUD_ENDPOINT", env.Server.URL,
		"HCLOUD_TOKEN", "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jN_NOT_VALID_dzhepnahq",
		"HCLOUD_METRICS_ENABLED", "false",
	)
	defer resetEnv()
	env.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(
			schema.ErrorResponse{
				Error: schema.Error{
					Code:    "unauthorized",
					Message: "unable to authenticate",
				},
			},
		)
	})

	_, err := newCloud(&bytes.Buffer{})
	assert.EqualError(t, err, "hcloud/newCloud: unable to authenticate (unauthorized)")
}

func TestCloud(t *testing.T) {
	env := newTestEnv()
	defer env.Teardown()

	resetEnv := Setenv(t,
		"HCLOUD_ENDPOINT", env.Server.URL,
		"HCLOUD_TOKEN", "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jN_NOT_VALID_dzhepnahq",
		"HCLOUD_METRICS_ENABLED", "false",
		"ROBOT_USER_NAME", "user",
		"ROBOT_PASSWORD", "pass123",
	)
	defer resetEnv()
	env.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(
			schema.ServerListResponse{
				Servers: []schema.Server{
					{
						ID:              1,
						Name:            "test",
						Status:          "running",
						Created:         time.Time{},
						PublicNet:       schema.ServerPublicNet{},
						PrivateNet:      nil,
						ServerType:      schema.ServerType{},
						IncludedTraffic: 0,
						OutgoingTraffic: nil,
						IngoingTraffic:  nil,
						BackupWindow:    nil,
						RescueEnabled:   false,
						ISO:             nil,
						Locked:          false,
						Datacenter:      schema.Datacenter{},
						Image:           nil,
						Protection:      schema.ServerProtection{},
						Labels:          nil,
						Volumes:         nil,
					},
				},
			},
		)
	})
	env.Mux.HandleFunc("/networks/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(
			schema.NetworkGetResponse{
				Network: schema.Network{
					ID:         1,
					Name:       "test",
					Created:    time.Time{},
					IPRange:    "10.0.0.8",
					Subnets:    nil,
					Routes:     nil,
					Servers:    nil,
					Protection: schema.NetworkProtection{},
					Labels:     nil,
				},
			},
		)
	})

	cloud, err := newCloud(&bytes.Buffer{})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	t.Run("Instances", func(t *testing.T) {
		_, supported := cloud.Instances()
		if supported {
			t.Error("Instances interface should not be supported")
		}
	})

	t.Run("Zones", func(t *testing.T) {
		_, supported := cloud.Zones()
		if supported {
			t.Error("Zones interface should not be supported")
		}
	})

	t.Run("InstancesV2", func(t *testing.T) {
		_, supported := cloud.InstancesV2()
		if !supported {
			t.Error("InstancesV2 interface should be supported")
		}
	})

	t.Run("LoadBalancer", func(t *testing.T) {
		_, supported := cloud.LoadBalancer()
		if !supported {
			t.Error("LoadBalancer interface should be supported")
		}
	})

	t.Run("Clusters", func(t *testing.T) {
		_, supported := cloud.Clusters()
		if supported {
			t.Error("Clusters interface should not be supported")
		}
	})

	t.Run("Routes", func(t *testing.T) {
		_, supported := cloud.Routes()
		if supported {
			t.Error("Routes interface should not be supported")
		}
	})

	t.Run("RoutesWithNetworks", func(t *testing.T) {
		resetEnv := Setenv(t,
			"HCLOUD_NETWORK", "1",
			"HCLOUD_NETWORK_DISABLE_ATTACHED_CHECK", "true",
			"HCLOUD_METRICS_ENABLED", "false",
		)
		defer resetEnv()

		c, err := newCloud(&bytes.Buffer{})
		if err != nil {
			t.Errorf("%s", err)
		}
		_, supported := c.Routes()
		if !supported {
			t.Error("Routes interface should be supported")
		}
	})

	t.Run("HasClusterID", func(t *testing.T) {
		if cloud.HasClusterID() {
			t.Error("HasClusterID should be false")
		}
	})

	t.Run("ProviderName", func(t *testing.T) {
		if cloud.ProviderName() != "hcloud" {
			t.Error("ProviderName should be hcloud")
		}
	})
}

func TestLoadBalancerDefaultsFromEnv(t *testing.T) {
	cases := []struct {
		name                     string
		env                      map[string]string
		expDefaults              hcops.LoadBalancerDefaults
		expDisablePrivateIngress bool
		expDisableIPv6           bool
		expErr                   string
	}{
		{
			name:        "None set",
			env:         map[string]string{},
			expDefaults: hcops.LoadBalancerDefaults{
				// strings should be empty (zero value)
				// bools should be false (zero value)
			},
		},
		{
			name: "All set (except network zone)",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_LOCATION":                "hel1",
				"HCLOUD_LOAD_BALANCERS_DISABLE_PRIVATE_INGRESS": "true",
				"HCLOUD_LOAD_BALANCERS_DISABLE_IPV6":            "true",
				"HCLOUD_LOAD_BALANCERS_USE_PRIVATE_IP":          "true",
			},
			expDefaults: hcops.LoadBalancerDefaults{
				Location:     "hel1",
				UsePrivateIP: true,
			},
			expDisablePrivateIngress: true,
			expDisableIPv6:           true,
		},
		{
			name: "Network zone set",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_NETWORK_ZONE": "eu-central",
			},
			expDefaults: hcops.LoadBalancerDefaults{
				NetworkZone: "eu-central",
			},
		},
		{
			name: "Both location and network zone set (error)",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_LOCATION":     "hel1",
				"HCLOUD_LOAD_BALANCERS_NETWORK_ZONE": "eu-central",
			},
			expErr: "HCLOUD_LOAD_BALANCERS_LOCATION/HCLOUD_LOAD_BALANCERS_NETWORK_ZONE: Only one of these can be set",
		},
		{
			name: "Invalid DISABLE_PRIVATE_INGRESS",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_DISABLE_PRIVATE_INGRESS": "invalid",
			},
			expErr: `HCLOUD_LOAD_BALANCERS_DISABLE_PRIVATE_INGRESS: strconv.ParseBool: parsing "invalid": invalid syntax`,
		},
		{
			name: "Invalid DISABLE_IPV6",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_DISABLE_IPV6": "invalid",
			},
			expErr: `HCLOUD_LOAD_BALANCERS_DISABLE_IPV6: strconv.ParseBool: parsing "invalid": invalid syntax`,
		},
		{
			name: "Invalid USE_PRIVATE_IP",
			env: map[string]string{
				"HCLOUD_LOAD_BALANCERS_USE_PRIVATE_IP": "invalid",
			},
			expErr: `HCLOUD_LOAD_BALANCERS_USE_PRIVATE_IP: strconv.ParseBool: parsing "invalid": invalid syntax`,
		},
	}

	for _, c := range cases {
		c := c // prevent scopelint from complaining
		t.Run(c.name, func(t *testing.T) {
			previousEnvVars := map[string]string{}
			unsetEnvVars := []string{}

			for k, v := range c.env {
				// Store previous value, so we can later restore it and not affect other tests in this package.
				if v, ok := os.LookupEnv(k); ok {
					previousEnvVars[k] = v
				} else if !ok {
					unsetEnvVars = append(unsetEnvVars, k)
				}
				os.Setenv(k, v)
			}

			// Make sure this is always executed, even on panic
			defer func() {
				for k, v := range previousEnvVars {
					os.Setenv(k, v)
				}
				for _, k := range unsetEnvVars {
					os.Unsetenv(k)
				}
			}()

			defaults, disablePrivateIngress, disableIPv6, err := loadBalancerDefaultsFromEnv()

			if c.expErr != "" {
				assert.EqualError(t, err, c.expErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, c.expDefaults, defaults)
			assert.Equal(t, c.expDisablePrivateIngress, disablePrivateIngress)
			assert.Equal(t, c.expDisableIPv6, disableIPv6)
		})
	}
}

func Test_updateHcloudCredentials(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	os.Unsetenv("HCLOUD_TOKEN")
	t.Setenv("HCLOUD_ENDPOINT", server.URL)
	t.Setenv("HCLOUD_METRICS_ENABLED", "false")

	rootDir, err := os.MkdirTemp("", "Test_newHcloudClient-*")
	require.NoError(t, err)

	credentialsDir := credentials.GetDirectory(rootDir)
	err = os.MkdirAll(credentialsDir, 0o755)
	require.NoError(t, err)

	token := "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jNZXCeTYQ4uArypFM3nh75"
	err = writeCredentials(credentialsDir, token)
	require.NoError(t, err)
	hcloudClient, err := newHcloudClient(rootDir)
	require.NoError(t, err)

	err = credentials.Watch(credentialsDir, hcloudClient, nil)
	require.NoError(t, err)

	hcloud.WithEndpoint(server.URL)(hcloudClient)

	mux.HandleFunc("/servers/1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, r.Header.Get("Authorization"), "Bearer "+token)
		json.NewEncoder(w).Encode(schema.ServerGetResponse{
			Server: schema.Server{
				ID:   1,
				Name: "foobar",
			},
		})
	})
	i := newInstances(hcloudClient, nil, AddressFamilyIPv4, 0)

	node := &corev1.Node{
		Spec: corev1.NodeSpec{ProviderID: "hcloud://1"},
	}
	_, err = i.InstanceExists(context.TODO(), node)
	require.NoError(t, err)

	oldCounter := credentials.GetHcloudReloadCounter()
	token2 := "22222ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jNZXCeTYQ4uArypFM3nh75"
	err = writeCredentials(credentialsDir, token2)
	require.NoError(t, err)
	start := time.Now()
	for {
		if credentials.GetHcloudReloadCounter() > oldCounter {
			break
		}
		if time.Since(start) > time.Second*3 {
			t.Fatal("timeout waiting for reload")
		}
		time.Sleep(time.Millisecond * 100)
	}

	mux.HandleFunc("/servers/2", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, r.Header.Get("Authorization"), "Bearer "+token2)
		json.NewEncoder(w).Encode(schema.ServerGetResponse{
			Server: schema.Server{
				ID:   2,
				Name: "foobar2",
			},
		})
	})
	node2 := &corev1.Node{
		Spec: corev1.NodeSpec{ProviderID: "hcloud://2"},
	}
	_, err = i.InstanceExists(context.TODO(), node2)
	require.NoError(t, err)
}

func writeCredentials(credentialsDir, token string) error {
	return os.WriteFile(filepath.Join(credentialsDir, "hcloud"),
		[]byte(token), 0o600)
}

func Test_EnsureLoadBalancer(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)

	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(
			schema.ServerListResponse{
				Servers: []schema.Server{},
			},
		)
	})
	schemaLB := schema.LoadBalancer{
		ID:   0,
		Name: "mylb",
	}
	mux.HandleFunc("/load_balancers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(
			schema.LoadBalancerListResponse{
				LoadBalancers: []schema.LoadBalancer{schemaLB},
			},
		)
	})
	mux.HandleFunc("/robot/server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]models.ServerResponse{
			{
				Server: models.Server{
					ServerIP:      "123.123.123.123",
					ServerIPv6Net: "2a01:f48:111:4221::",
					ServerNumber:  321,
					Name:          "bm-server1",
				},
			},
		})
	})

	t.Setenv("HCLOUD_ENDPOINT", server.URL)
	t.Setenv("HCLOUD_DEBUG", "true")
	t.Setenv("HCLOUD_TOKEN", "jr5g7ZHpPptyhJzZyHw2Pqu4g9gTqDvEceYpngPf79jN_NOT_VALID_dzhepnahq")
	t.Setenv("ROBOT_USER_NAME", "user")
	t.Setenv("ROBOT_PASSWORD", "pass123")
	t.Setenv("ROBOT_ENDPOINT", server.URL+"/robot")
	t.Setenv("HCLOUD_METRICS_ENABLED", "false")

	cloud, err := newCloud(&bytes.Buffer{})
	require.NoError(t, err)
	lb, supported := cloud.LoadBalancer()
	require.True(t, supported)
	_, err = lb.EnsureLoadBalancer(context.Background(), "dummy", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				string(annotation.LBLocation): "hel1",
			},
		},
	}, nil)
	require.NoError(t, err)
}
