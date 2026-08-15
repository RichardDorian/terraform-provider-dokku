package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ datasource.DataSource              = &AppDockerImageDataSource{}
	_ datasource.DataSourceWithConfigure = &AppDockerImageDataSource{}
)

func NewAppDockerImageDataSource() datasource.DataSource { return &AppDockerImageDataSource{} }

// AppDockerImageDataSource reads back the image backing an app's running
// containers via `dokku ps:inspect` (a sanitized wrapper around `docker
// inspect`). Dokku builds a single image per app and runs every process type
// (web, worker, ...) from it, so any one running container reflects the
// image for the whole app.
type AppDockerImageDataSource struct {
	client *dokku.Client
}

type AppDockerImageDataSourceModel struct {
	App               types.String `tfsdk:"app"`
	NullIfNotDeployed types.Bool   `tfsdk:"null_if_not_deployed"`
	Image             types.String `tfsdk:"image"`
	ImageID           types.String `tfsdk:"image_id"`
	ID                types.String `tfsdk:"id"`
}

// errNotDeployed indicates the app doesn't exist or has no running
// containers, as opposed to an error parsing a response that was
// successfully retrieved.
var errNotDeployed = errors.New("app does not exist or has no running containers")

// dockerInspectContainer captures the subset of `docker inspect` output
// (as relayed by `dokku ps:inspect`) needed to identify a container's image.
type dockerInspectContainer struct {
	Id     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
}

func (d *AppDockerImageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_docker_image"
}

func (d *AppDockerImageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the image currently backing an app's running containers (`dokku ps:inspect`). Requires the app to have at least one running container.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required:    true,
				Description: "App to inspect.",
			},
			"null_if_not_deployed": schema.BoolAttribute{
				Optional:    true,
				Description: "If true, set `image`, `image_id`, and `id` to null instead of failing when the app doesn't exist or has no running containers.",
			},
			"image": schema.StringAttribute{
				Computed:    true,
				Description: "Image reference the app's running container was created from (`docker inspect`'s `Config.Image`), e.g. \"dokku/my-app:latest\".",
			},
			"image_id": schema.StringAttribute{
				Computed:    true,
				Description: "Content-addressed ID of the image backing the app's running container (`docker inspect`'s `Image` field), e.g. \"sha256:...\".",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The ID of this resource.",
			},
		},
	}
}

func (d *AppDockerImageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dokku.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source configure type", "Expected *dokku.Client")
		return
	}
	d.client = client
}

func (d *AppDockerImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AppDockerImageDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	container, err := d.deployedContainer(ctx, app)
	if err != nil {
		if errors.Is(err, errNotDeployed) && data.NullIfNotDeployed.ValueBool() {
			data.Image = types.StringNull()
			data.ImageID = types.StringNull()
			data.ID = types.StringNull()
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
		resp.Diagnostics.AddError("Error reading deployed image", err.Error())
		return
	}

	data.Image = types.StringValue(container.Config.Image)
	data.ImageID = types.StringValue(container.Image)
	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *AppDockerImageDataSource) deployedContainer(ctx context.Context, app string) (*dockerInspectContainer, error) {
	res, err := d.client.Run(ctx, "ps:inspect", app)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%w: dokku ps:inspect %s: exit %d: %s%s", errNotDeployed, app, res.ExitCode, res.Stderr, res.Stdout)
	}

	var containers []dockerInspectContainer
	if err := json.Unmarshal([]byte(res.Stdout), &containers); err != nil {
		return nil, fmt.Errorf("parsing ps:inspect output: %w (output: %s)", err, res.Stdout)
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("%w", errNotDeployed)
	}
	return &containers[0], nil
}
