package authentik

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/sethvargo/go-password/password"
	"github.com/trustacks/catalog/pkg/catalog"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// componentName is the name of the component.
	componentName = "authentik"
	// serviceURL is the url of the authentik service.
	serviceURL = "http://authentik"
)

// apiTokenSecret is the secret where the api token is stored.
var apiTokenSecret = "authentik-bootstrap"

type authentik struct {
	catalog.BaseComponent
}

// preInstall creates the authentik admin api token.
func (c *authentik) preInstall() error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	log.Printf("create admin api token")
	res, err := password.Generate(32, 10, 0, false, false)
	if err != nil {
		return err
	}
	if err := createAPIToken(res, clientset); err != nil {
		return err
	}
	return nil
}

// postInstall creates the authentik user groups.
func (c *authentik) postInstall() error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	log.Println("create authentik user groups")
	token, err := getAPIToken(clientset)
	if err != nil {
		return err
	}
	if err := createGroups(serviceURL, token); err != nil {
		return err
	}
	return nil
}

// createAPIToken creates the api token secret.
func createAPIToken(token string, clientset kubernetes.Interface) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiTokenSecret,
		},
		Data: map[string][]byte{
			"api-token": []byte(token),
		},
	}
	namespace, err := getNamespace()
	if err != nil {
		return err
	}
	_, err = clientset.CoreV1().Secrets(namespace).Get(context.TODO(), apiTokenSecret, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			_, err = clientset.CoreV1().Secrets(namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
			return err
		}
		if !strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// group represents an authentik group.
type group struct {
	Name        string `json:"name"`
	Users       []int  `json:"users"`
	IsSuperuser bool   `json:"is_superuser"`
	Parent      *int   `json:"parent"`
}

// createGroups creates the user groups.
func createGroups(url, token string) error {
	groups := []group{
		{"admins", []int{1}, true, nil},
		{"editors", []int{}, false, nil},
		{"viewers", []int{}, false, nil},
	}
	for _, g := range groups {
		data, err := json.Marshal(g)
		if err != nil {
			return err
		}
		// check if the group already exists.
		resp, err := getAPIResource(url, "core/groups", token, fmt.Sprintf("name=%s", g.Name))
		if err != nil {
			return err
		}
		results := make(map[string]interface{})
		if err := json.Unmarshal(resp, &results); err != nil {
			return err
		}
		if len(results["results"].([]interface{})) > 0 {
			continue
		}
		_, err = postAPIResource(url, "core/groups", token, data)
		if err != nil {
			return err
		}
	}
	return nil
}

// getAPIToken gets the api token secret value.
func getAPIToken(clientset kubernetes.Interface) (string, error) {
	namespace, err := getNamespace()
	if err != nil {
		return "", err
	}
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), apiTokenSecret, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret.Data["api-token"])), nil
}

// getAPIResource gets the API resource at the provided path.
func getAPIResource(url, resource, token string, search string) ([]byte, error) {
	uri := fmt.Sprintf("%s/api/v3/%s/", url, resource)
	if search != "" {
		uri = fmt.Sprintf("%s?%s", uri, search)
	}
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("'%s' get error: %s", resource, body)
	}
	return body, nil
}

// postAPIResource posts the API resource at the provided path.
func postAPIResource(url, resource, token string, data []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v3/%s/", url, resource), bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("'%s' post error: %s", resource, body)
	}
	return body, nil
}

// getNamespace gets the current kubernetes namespace.
func getNamespace() (string, error) {
	data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), err
}

type propertyMapping struct {
	PK      string `json:"pk"`
	Managed string `json:"managed"`
}

type propertyMappings struct {
	Results []propertyMapping `json:"results"`
}

// getPropertyMappings gets the ids of the oauth2 scope mappings.
func getPropertyMappings(url, token string) ([]string, error) {
	scopes := []string{
		"goauthentik.io/providers/oauth2/scope-email",
		"goauthentik.io/providers/oauth2/scope-openid",
		"goauthentik.io/providers/oauth2/scope-profile",
	}
	pks := make([]string, len(scopes))
	resp, err := getAPIResource(url, "propertymappings/all", token, "")
	if err != nil {
		return nil, err
	}
	pm := &propertyMappings{}
	if err := json.Unmarshal(resp, &pm); err != nil {
		return nil, err
	}
	for _, p := range pm.Results {
		for i, scope := range scopes {
			if p.Managed == scope {
				pks[i] = p.PK
			}
		}
	}
	return pks, nil
}

type flow struct {
	PK   string `json:"pk"`
	Slug string `json:"slug"`
}

type flows struct {
	Results []flow `json:"results"`
}

// getAuthorizationFlow gets the id of the default authorization
// flow.
func getAuthorizationFlow(url, token string) (string, error) {
	resp, err := getAPIResource(url, "flows/instances", token, "")
	if err != nil {
		return "", err
	}
	f := &flows{}
	if err := json.Unmarshal(resp, &f); err != nil {
		return "", err
	}
	for _, flow := range f.Results {
		if flow.Slug == "default-provider-authorization-explicit-consent" {
			return flow.PK, nil
		}
	}
	return "", errors.New("authorization flow not found")
}

// createOIDCProvier creates a new openid connection auth provider.
func createOIDCProvider(name, url, token, flow string, mappings []string) (int, string, string, error) {
	client_id, err := password.Generate(40, 30, 0, false, true)
	if err != nil {
		return -1, "", "", err
	}
	client_secret, err := password.Generate(128, 96, 0, false, true)
	if err != nil {
		return -1, "", "", err
	}
	body := map[string]interface{}{
		"name":               name,
		"authorization_flow": flow,
		"client_type":        "confidential",
		"client_id":          client_id,
		"client_secret":      client_secret,
		"property_mappings":  mappings,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return -1, "", "", err
	}
	resp, err := postAPIResource(url, "providers/oauth2", token, data)
	if err != nil {
		return -1, "", "", err
	}
	provider := map[string]interface{}{}
	if err := json.Unmarshal(resp, &provider); err != nil {
		return -1, "", "", err
	}
	return int(provider["pk"].(float64)), client_id, client_secret, nil
}

// createApplication creates a new application.
func createApplication(provider int, name, url, token string) error {
	body := map[string]interface{}{
		"name":     name,
		"slug":     name,
		"provider": provider,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = postAPIResource(url, "core/applications", token, data)
	return err
}

// CreateOIDCCLient creates a consumable end to end oidc client.
func CreateOIDCCLient(name string) (interface{}, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	token, err := getAPIToken(clientset)
	if err != nil {
		return nil, err
	}
	mappings, err := getPropertyMappings(serviceURL, token)
	if err != nil {
		return nil, err
	}
	flow, err := getAuthorizationFlow(serviceURL, token)
	if err != nil {
		return nil, err
	}
	pk, id, secret, err := createOIDCProvider(name, serviceURL, token, flow, mappings)
	if err != nil {
		return nil, err
	}
	if err := createApplication(pk, name, serviceURL, token); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"issuer":        fmt.Sprintf("http://authentik.local.gd:8081/application/o/%s/", name),
		"client_id":     id,
		"client_secret": secret,
	}, nil
}

//go:embed config.yaml
var config []byte

// Initialize adds the component to the catalog and configures hooks.
func Initialize(c *catalog.ComponentCatalog) {
	var conf *catalog.ComponentConfig
	if err := yaml.Unmarshal(config, &conf); err != nil {
		log.Fatal(err)
	}
	component := &authentik{
		catalog.BaseComponent{
			Repo:    conf.Repo,
			Chart:   conf.Chart,
			Version: conf.Version,
			Values:  conf.Values,
			Hooks:   conf.Hooks,
		},
	}
	c.AddComponent(componentName, component)

	for hook, fn := range map[string]func() error{
		"preInstall":  component.preInstall,
		"postInstall": component.postInstall,
	} {
		if err := catalog.AddHook(componentName, hook, fn); err != nil {
			log.Fatal(err)
		}
	}
}