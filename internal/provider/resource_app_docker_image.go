package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource                = &AppDockerImageResource{}
	_ resource.ResourceWithConfigure   = &AppDockerImageResource{}
	_ resource.ResourceWithImportState = &AppDockerImageResource{}
)

func NewAppDockerImageResource() resource.Resource { return &AppDockerImageResource{} }

// AppDockerImageResource deploys a Dokku app straight from a container
// registry image, bypassing git push (`dokku git:from-image`). Dokku
// generates a single-line Dockerfile (`FROM <image>`), commits it to the
// app's repo and deploys it.
type AppDockerImageResource struct {
	client *dokku.Client
}

type AppDockerImageResourceModel struct {
	App              types.String `tfsdk:"app"`
	Image            types.String `tfsdk:"image"`
	RegistryUsername types.String `tfsdk:"registry_username"`
	RegistryPassword types.String `tfsdk:"registry_password"`
	DeployedSHA      types.String `tfsdk:"deployed_sha"`
	NetworkAlias     types.String `tfsdk:"network_alias"`
	ID               types.String `tfsdk:"id"`
}

func (r *AppDockerImageResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_docker_image"
}

func (r *AppDockerImageResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Deploys a Dokku app from a container registry image (`dokku git:from-image`), instead of a git push. Changing `image` triggers a redeploy.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required:    true,
				Description: "App to deploy.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image": schema.StringAttribute{
				Required:    true,
				Description: "Image reference to deploy, e.g. \"my-registry/node-js-getting-started:latest\". Pin to a digest (`image@sha256:...`) to guarantee a redeploy when the tag is reused with new content.",
			},
			"registry_username": schema.StringAttribute{
				Optional:    true,
				WriteOnly:   true,
				Description: "Username used to authenticate to the private registry hosting `image` before deploying (`dokku registry:login`). The registry host is inferred from `image`. Required together with registry_password. Write-only: not persisted in plan or state, so a credential change alone will not trigger a redeploy.",
			},
			"registry_password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				WriteOnly:   true,
				Description: "Password or access token used to authenticate to the private registry hosting `image`. Required together with registry_username. Write-only: not persisted in plan or state, so a credential change alone will not trigger a redeploy.",
			},
			"deployed_sha": schema.StringAttribute{
				Computed:    true,
				Description: "Deploy revision reported by Dokku for the current deploy.",
			},
			"network_alias": schema.StringAttribute{
				Computed:    true,
				Description: "Dyno identifier Dokku assigns to the app's primary web container (`<app>.web.1`), as seen in `com.dokku.dyno` container labels and `dokku logs` line prefixes. Populated once the resource has been deployed. Dokku appends a random suffix to the actual underlying `docker ps` container name that isn't exposed by any `dokku` command, so this is the stable identifier rather than the literal container name.",
			},
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *AppDockerImageResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dokku.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected resource configure type", "Expected *dokku.Client")
		return
	}
	r.client = client
}

// registryHost infers the registry host to authenticate against from an
// image reference, following the same convention as `docker pull`: the
// segment before the first "/" is a host only if it looks like one
// (contains "." or ":", or is "localhost"); otherwise the image is assumed
// to live on Docker Hub.
func registryHost(image string) string {
	repo, _, _ := strings.Cut(image, "/")
	if repo != image && (strings.ContainsAny(repo, ".:") || repo == "localhost") {
		return repo
	}
	return "docker.io"
}

// deploy logs in to the registry hosting image (if credentials were
// supplied), deploys it, then logs back out so the credentials aren't left
// behind in Dokku's registry auth config. username and password come from
// the request config rather than the resource model: they're write-only
// attributes, so Terraform never populates them on the plan or state values
// the model is otherwise read from.
func (r *AppDockerImageResource) deploy(ctx context.Context, data *AppDockerImageResourceModel, username, password string) (err error) {
	if username == "" && password == "" {
		_, err = r.client.RunChecked(ctx, "git:from-image", data.App.ValueString(), data.Image.ValueString())
		return err
	}
	if username == "" || password == "" {
		return errors.New("registry_username and registry_password must be set together")
	}

	host := registryHost(data.Image.ValueString())
	if _, err = r.client.RunChecked(ctx, "registry:login", host, username, password); err != nil {
		return err
	}
	defer func() {
		if _, logoutErr := r.client.RunChecked(ctx, "registry:logout", host); logoutErr != nil {
			err = errors.Join(err, logoutErr)
		}
	}()

	_, err = r.client.RunChecked(ctx, "git:from-image", data.App.ValueString(), data.Image.ValueString())
	return err
}

func (r *AppDockerImageResource) populateComputed(ctx context.Context, data *AppDockerImageResourceModel) {
	report, err := r.client.Report(ctx, "git", data.App.ValueString())
	if err != nil {
		data.DeployedSHA = types.StringValue("")
	} else {
		data.DeployedSHA = types.StringValue(report["sha"])
	}
	data.NetworkAlias = types.StringValue(data.App.ValueString() + ".web")
}

func (r *AppDockerImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppDockerImageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config AppDockerImageResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deploy(ctx, &data, config.RegistryUsername.ValueString(), config.RegistryPassword.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deploying app from image", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	r.populateComputed(ctx, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppDockerImageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.Report(ctx, "apps", data.App.ValueString()); err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	r.populateComputed(ctx, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppDockerImageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config AppDockerImageResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deploy(ctx, &plan, config.RegistryUsername.ValueString(), config.RegistryPassword.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error redeploying app from image", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.App.ValueString())
	r.populateComputed(ctx, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete only drops the resource from state: Dokku has no inverse of
// git:from-image, so there is nothing to roll back on the app itself.
func (r *AppDockerImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}

func (r *AppDockerImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
