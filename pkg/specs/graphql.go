package specs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	multierror "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/replicatedhq/ship/pkg/api"
	"github.com/spf13/viper"
)

const getAppspecQuery = `
query($semver: String) {
  shipRelease (semver: $semver) {
    id
    channelId
    channelName
    channelIcon
    semver
    releaseNotes
    spec
    images {
      url
      source
      appSlug
      imageKey
    }
    created
    registrySecret
  }
}`

// GraphQLClient is a client for the graphql Payload API
type GraphQLClient struct {
	GQLServer *url.URL
	Client    *http.Client
}

// GraphQLRequest is a json-serializable request to the graphql server
type GraphQLRequest struct {
	Query         string            `json:"query"`
	Variables     map[string]string `json:"variables"`
	OperationName string            `json:"operationName"`
}

// GraphQLError represents an error returned by the graphql server
type GraphQLError struct {
	Locations []map[string]interface{} `json:"locations"`
	Message   string                   `json:"message"`
	Code      string                   `json:"code"`
}

// GQLGetReleaseResponse is the top-level response object from the graphql server
type GQLGetReleaseResponse struct {
	Data   ShipReleaseWrapper `json:"data,omitempty"`
	Errors []GraphQLError     `json:"errors,omitempty"`
}

// ShipReleaseWrapper wraps the release response form GQL
type ShipReleaseWrapper struct {
	ShipRelease ShipRelease `json:"shipRelease"`
}

type Image struct {
	URL      string `json:"url"`
	Source   string `json:"source"`
	AppSlug  string `json:"appSlug"`
	ImageKey string `json:"imageKey"`
}

// ShipRelease is the release response form GQL
type ShipRelease struct {
	ID             string  `json:"id"`
	ChannelID      string  `json:"channelId"`
	ChannelName    string  `json:"channelName"`
	ChannelIcon    string  `json:"channelIcon"`
	Semver         string  `json:"semver"`
	ReleaseNotes   string  `json:"releaseNotes"`
	Spec           string  `json:"spec"`
	Images         []Image `json:"images"`
	Created        string  `json:"created"` // TODO: this time is not in RFC 3339 format
	RegistrySecret string  `json:"registrySecret"`
}

// GQLRegisterInstallResponse is the top-level response object from the graphql server
type GQLRegisterInstallResponse struct {
	Data struct {
		ShipRegisterInstall bool `json:"shipRegisterInstall"`
	} `json:"data,omitempty"`
	Errors []GraphQLError `json:"errors,omitempty"`
}

type callInfo struct {
	username string
	password string
	request  GraphQLRequest
}

// ToReleaseMeta linter
func (r *ShipRelease) ToReleaseMeta() api.ReleaseMetadata {
	return api.ReleaseMetadata{
		ReleaseID:      r.ID,
		ChannelID:      r.ChannelID,
		ChannelName:    r.ChannelName,
		ChannelIcon:    r.ChannelIcon,
		Semver:         r.Semver,
		ReleaseNotes:   r.ReleaseNotes,
		Created:        r.Created,
		RegistrySecret: r.RegistrySecret,
		Images:         r.apiImages(),
	}
}

func (r *ShipRelease) apiImages() []api.Image {
	result := []api.Image{}
	for _, image := range r.Images {
		result = append(result, api.Image(image))
	}
	return result
}

// NewGraphqlClient builds a new client using a viper instance
func NewGraphqlClient(v *viper.Viper) (*GraphQLClient, error) {
	addr := v.GetString("customer-endpoint")
	server, err := url.ParseRequestURI(addr)
	if err != nil {
		return nil, errors.Wrapf(err, "parse GQL server address %s", addr)
	}
	return &GraphQLClient{
		GQLServer: server,
		Client:    http.DefaultClient,
	}, nil
}

// GetRelease gets a payload from the graphql server
func (c *GraphQLClient) GetRelease(customerID, installationID, semver string) (*ShipRelease, error) {
	requestObj := GraphQLRequest{
		Query: getAppspecQuery,
		Variables: map[string]string{
			"semver": semver,
		},
	}

	ci := callInfo{
		username: customerID,
		password: installationID,
		request:  requestObj,
	}

	shipResponse := &GQLGetReleaseResponse{}
	if err := c.callGQL(ci, shipResponse); err != nil {
		return nil, err
	}

	if shipResponse.Errors != nil && len(shipResponse.Errors) > 0 {
		var multiErr *multierror.Error
		for _, err := range shipResponse.Errors {
			multiErr = multierror.Append(multiErr, fmt.Errorf("%s: %s", err.Code, err.Message))

		}
		return nil, multiErr.ErrorOrNil()
	}

	return &shipResponse.Data.ShipRelease, nil
}

func (c *GraphQLClient) RegisterInstall(customerID, installationID, channelID, releaseID string) error {
	requestObj := GraphQLRequest{
		Query: `
mutation($channelId: String!, $releaseId: String!) {
  shipRegisterInstall(
    channelId: $channelId
    releaseId: $releaseId
  )
}`,
		Variables: map[string]string{
			"channelId": channelID,
			"releaseId": releaseID,
		},
	}

	ci := callInfo{
		username: customerID,
		password: installationID,
		request:  requestObj,
	}

	shipResponse := &GQLRegisterInstallResponse{}
	if err := c.callGQL(ci, shipResponse); err != nil {
		return err
	}

	if shipResponse.Errors != nil && len(shipResponse.Errors) > 0 {
		var multiErr *multierror.Error
		for _, err := range shipResponse.Errors {
			multiErr = multierror.Append(multiErr, fmt.Errorf("%s: %s", err.Code, err.Message))

		}
		return multiErr.ErrorOrNil()
	}

	return nil
}

func (c *GraphQLClient) callGQL(ci callInfo, result interface{}) error {
	body, err := json.Marshal(ci.request)
	if err != nil {
		return errors.Wrap(err, "marshal request")
	}

	bodyReader := ioutil.NopCloser(bytes.NewReader(body))
	authString := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", ci.username, ci.password)))

	graphQLRequest, err := http.NewRequest(http.MethodPost, c.GQLServer.String(), bodyReader)

	graphQLRequest.Header = map[string][]string{
		"Authorization": {"Basic " + authString},
		"Content-Type":  {"application/json"},
	}

	resp, err := c.Client.Do(graphQLRequest)
	if err != nil {
		return errors.Wrap(err, "send request")
	}

	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read body")
	}

	if err := json.Unmarshal(responseBody, result); err != nil {
		return errors.Wrapf(err, "unmarshal response %s", responseBody)
	}

	return nil
}
