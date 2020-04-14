package azure

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/tombuildsstuff/giovanni/storage/2018-11-09/blob/blobs"
	"github.com/tombuildsstuff/giovanni/storage/2018-11-09/blob/containers"

	"github.com/Azure/azure-sdk-for-go/profiles/2017-03-09/resources/mgmt/resources"
	armStorage "github.com/Azure/azure-sdk-for-go/profiles/2017-03-09/storage/mgmt/storage"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/hashicorp/go-azure-helpers/authentication"
	"github.com/hashicorp/go-azure-helpers/sender"
	"github.com/hashicorp/terraform/httpclient"
)

type ArmClient struct {
	// These Clients are only initialized if an Access Key isn't provided
	groupsClient          *resources.GroupsClient
	storageAccountsClient *armStorage.AccountsClient
	containersClient      *containers.Client
	blobsClient           *blobs.Client

	accessKey          string
	environment        azure.Environment
	resourceGroupName  string
	storageAccountName string
	sasToken           string
}

func buildArmClient(config BackendConfig) (*ArmClient, error) {
	env, err := buildArmEnvironment(config)
	if err != nil {
		return nil, err
	}

	client := ArmClient{
		environment:        *env,
		resourceGroupName:  config.ResourceGroupName,
		storageAccountName: config.StorageAccountName,
	}

	// if we have an Access Key - we don't need the other clients
	if config.AccessKey != "" {
		client.accessKey = config.AccessKey
		return &client, nil
	}

	// likewise with a SAS token
	if config.SasToken != "" {
		client.sasToken = config.SasToken
		return &client, nil
	}

	builder := authentication.Builder{
		ClientID:                      config.ClientID,
		ClientSecret:                  config.ClientSecret,
		SubscriptionID:                config.SubscriptionID,
		TenantID:                      config.TenantID,
		CustomResourceManagerEndpoint: config.CustomResourceManagerEndpoint,
		Environment:                   config.Environment,
		MsiEndpoint:                   config.MsiEndpoint,

		// Feature Toggles
		SupportsAzureCliToken:          true,
		SupportsClientSecretAuth:       true,
		SupportsManagedServiceIdentity: config.UseMsi,
		// TODO: support for Client Certificate auth
	}
	armConfig, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("Error building ARM Config: %+v", err)
	}

	oauthConfig, err := armConfig.BuildOAuthConfig(env.ActiveDirectoryEndpoint)
	if err != nil {
		return nil, err
	}

	auth, err := armConfig.GetAuthorizationToken(sender.BuildSender("backend/remote-state/azure"), oauthConfig, env.TokenAudience)
	if err != nil {
		return nil, err
	}

	accountsClient := armStorage.NewAccountsClientWithBaseURI(env.ResourceManagerEndpoint, armConfig.SubscriptionID)
	client.configureClient(&accountsClient.Client, auth)
	client.storageAccountsClient = &accountsClient

	groupsClient := resources.NewGroupsClientWithBaseURI(env.ResourceManagerEndpoint, armConfig.SubscriptionID)
	client.configureClient(&groupsClient.Client, auth)
	client.groupsClient = &groupsClient

	return &client, nil
}

func buildArmEnvironment(config BackendConfig) (*azure.Environment, error) {
	if config.CustomResourceManagerEndpoint != "" {
		log.Printf("[DEBUG] Loading Environment from Endpoint %q", config.CustomResourceManagerEndpoint)
		return authentication.LoadEnvironmentFromUrl(config.CustomResourceManagerEndpoint)
	}

	log.Printf("[DEBUG] Loading Environment %q", config.Environment)
	return authentication.DetermineEnvironment(config.Environment)
}

func (c ArmClient) getGiovanniBlobClient(ctx context.Context) (*blobs.Client, error) {
	// TODO Figure out with SASToken?
	accessKey := c.accessKey
	if accessKey == "" {
		log.Printf("[DEBUG] Building the Blob Client from an Access Token (using user credentials)")
		keys, err := c.storageAccountsClient.ListKeys(ctx, c.resourceGroupName, c.storageAccountName)
		if err != nil {
			return nil, fmt.Errorf("Error retrieving keys for Storage Account %q: %s", c.storageAccountName, err)
		}

		if keys.Keys == nil {
			return nil, fmt.Errorf("Nil key returned for storage account %q", c.storageAccountName)
		}

		accessKeys := *keys.Keys
		accessKey = *accessKeys[0].Value
	}

	storageAuth, err := autorest.NewSharedKeyAuthorizer(c.storageAccountName, accessKey, autorest.SharedKey)
	if err != nil {
		return nil, fmt.Errorf("Error building Authorizer: %+v", err)
	}

	blobsClient := blobs.NewWithEnvironment(c.environment)
	blobsClient.Client.Authorizer = storageAuth
	return &blobsClient, nil
}

func (c ArmClient) getGiovanniContainersClient(ctx context.Context) (*containers.Client, error) {
	// TODO Figure out with SASToken?
	accessKey := c.accessKey
	if accessKey == "" {
		log.Printf("[DEBUG] Building the Blob Client from an Access Token (using user credentials)")
		keys, err := c.storageAccountsClient.ListKeys(ctx, c.resourceGroupName, c.storageAccountName)
		if err != nil {
			return nil, fmt.Errorf("Error retrieving keys for Storage Account %q: %s", c.storageAccountName, err)
		}

		if keys.Keys == nil {
			return nil, fmt.Errorf("Nil key returned for storage account %q", c.storageAccountName)
		}

		accessKeys := *keys.Keys
		accessKey = *accessKeys[0].Value
	}

	storageAuth, err := autorest.NewSharedKeyAuthorizer(c.storageAccountName, accessKey, autorest.SharedKey)
	if err != nil {
		return nil, fmt.Errorf("Error building Authorizer: %+v", err)
	}

	containersClient := containers.NewWithEnvironment(c.environment)
	containersClient.Client.Authorizer = storageAuth
	return &containersClient, nil
}

func (c *ArmClient) configureClient(client *autorest.Client, auth autorest.Authorizer) {
	client.UserAgent = buildUserAgent()
	client.Authorizer = auth
	client.Sender = buildSender()
	client.SkipResourceProviderRegistration = false
	client.PollingDuration = 60 * time.Minute
}

func buildUserAgent() string {
	userAgent := httpclient.UserAgentString()

	// append the CloudShell version to the user agent if it exists
	if azureAgent := os.Getenv("AZURE_HTTP_USER_AGENT"); azureAgent != "" {
		userAgent = fmt.Sprintf("%s %s", userAgent, azureAgent)
	}

	return userAgent
}
