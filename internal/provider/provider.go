package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

// Ensure DokkuProvider satisfies various provider interfaces.
var _ provider.Provider = &DokkuProvider{}

type DokkuProvider struct {
	version string
}

type DokkuProviderModel struct {
	Host           types.String `tfsdk:"host"`
	Port           types.String `tfsdk:"port"`
	SSHUser        types.String `tfsdk:"ssh_user"`
	PrivateKey     types.String `tfsdk:"private_key"`
	PrivateKeyPath types.String `tfsdk:"private_key_path"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &DokkuProvider{version: version}
	}
}

func (p *DokkuProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "dokku"
	resp.Version = p.version
}

func (p *DokkuProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages resources on a Dokku PaaS instance over SSH.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "Hostname or IP of the Dokku server. Can also be set via the DOKKU_HOST environment variable.",
			},
			"port": schema.StringAttribute{
				Optional:    true,
				Description: "SSH port of the Dokku server. Defaults to 22.",
			},
			"ssh_user": schema.StringAttribute{
				Optional:    true,
				Description: "SSH user used to reach the Dokku forced-command interface. Defaults to \"dokku\".",
			},
			"private_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "PEM-encoded SSH private key contents. Mutually exclusive with private_key_path. Can also be set via the DOKKU_PRIVATE_KEY environment variable.",
			},
			"private_key_path": schema.StringAttribute{
				Optional:    true,
				Description: "Path to a PEM-encoded SSH private key file. Mutually exclusive with private_key. Can also be set via the DOKKU_PRIVATE_KEY_PATH environment variable.",
			},
		},
	}
}

func (p *DokkuProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data DokkuProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	host := data.Host.ValueString()
	if host == "" {
		host = os.Getenv("DOKKU_HOST")
	}
	if host == "" {
		resp.Diagnostics.AddError("Missing host", "The dokku provider requires a host, set via the `host` attribute or DOKKU_HOST environment variable.")
		return
	}

	port := data.Port.ValueString()
	if port == "" {
		port = os.Getenv("DOKKU_PORT")
	}

	user := data.SSHUser.ValueString()
	if user == "" {
		user = os.Getenv("DOKKU_SSH_USER")
	}

	keyPEM := data.PrivateKey.ValueString()
	if keyPEM == "" {
		keyPEM = os.Getenv("DOKKU_PRIVATE_KEY")
	}

	keyPath := data.PrivateKeyPath.ValueString()
	if keyPath == "" {
		keyPath = os.Getenv("DOKKU_PRIVATE_KEY_PATH")
	}

	if keyPEM == "" && keyPath != "" {
		b, err := os.ReadFile(keyPath)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read private_key_path", err.Error())
			return
		}
		keyPEM = string(b)
	}

	if keyPEM == "" {
		resp.Diagnostics.AddError("Missing private key", "The dokku provider requires private_key or private_key_path (or DOKKU_PRIVATE_KEY / DOKKU_PRIVATE_KEY_PATH).")
		return
	}

	client, err := dokku.NewClient(dokku.Config{
		Host:          host,
		Port:          port,
		User:          user,
		PrivateKeyPEM: keyPEM,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create dokku client", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *DokkuProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewNetworkResource,
		NewStorageResource,
		NewStorageAssignmentResource,
		NewAppResource,
		NewAppDockerImageResource,
		NewAppDockerOptionsResource,
		NewAppDockerLabelResource,
		NewAppPortResource,
		NewAppDomainResource,
		NewAppEnvResource,
		NewAppScaleResource,
		NewAppSchedulerResource,
		NewAppNetworkResource,
		NewAppProxyResource,
		NewAppChecksResource,
		NewAppTraefikResource,
		NewAppLetsencryptResource,
		NewDomainResource,
		NewLetsencryptResource,
		NewNginxResource,
		NewPluginsResource,
		NewTraefikResource,
		NewPostgresResource,
		NewPostgresLinkResource,
		NewRedisResource,
		NewRedisLinkResource,
	}
}

func (p *DokkuProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewAppDockerImageDataSource,
	}
}
