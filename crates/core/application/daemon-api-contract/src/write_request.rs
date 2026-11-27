//! What every library write request carries besides its own fields, and
//! each request's target: shared by the server (which plans the write) and
//! the client (which fills its id and caller), so the impls are here, beside
//! the trait.

use daemon_proto::v1;

/// What every library write request carries besides its own fields.
/// (its caller: [`daemon_proto::CarriesMut`]).
pub trait LibraryWriteRequest: daemon_proto::CarriesMut {
    /// What it writes: a resource name, or a parent.
    fn target(&self) -> &str;
    /// The request, writing `target` (a client names its parent so).
    fn set_target(&mut self, target: String);
    /// The request, under the request id `id` (AIP-155), where it names none.
    fn set_request_id(&mut self, id: String);
    /// The client's id for the write (AIP-155): the command's id.
    fn request_id(&self) -> &str;
    fn validate_only(&self) -> bool;
    /// The etag the caller's preview answered (AIP-154: the path's last
    /// sequence, as a decimal), sent back with the apply; empty with
    /// `--yes` or for a write that takes none (a create).
    fn etag(&self) -> &str;
}

/// Each write request's target, as its message names it.
macro_rules! write_requests {
    ($($request:ident . $($field:ident).+ $(, $etag:ident)? ;)*) => {
        $(
            impl LibraryWriteRequest for v1::$request {
                // What the request writes, its resource or parent: a
                // request with a field of its own named `target` (a
                // materialize's, where it writes on disk) is not that.
                #[allow(clippy::misnamed_getters)]
                fn target(&self) -> &str {
                    write_requests!(@get self $($field).+)
                }
                fn set_target(&mut self, target: String) {
                    write_requests!(@set self target $($field).+)
                }
                fn set_request_id(&mut self, id: String) {
                    self.request_id = id;
                }
                fn request_id(&self) -> &str {
                    &self.request_id
                }
                fn validate_only(&self) -> bool {
                    self.validate_only
                }
                fn etag(&self) -> &str {
                    write_requests!(@etag self $($etag)?)
                }
            }
        )*
    };
    (@etag $self:ident etag) => { &$self.etag };
    (@etag $self:ident) => { "" };
    (@get $self:ident $field:ident) => { &$self.$field };
    (@get $self:ident $outer:ident . $inner:ident) => {
        $self.$outer.as_ref().map_or("", |outer| outer.$inner.as_str())
    };
    (@set $self:ident $target:ident $field:ident) => { $self.$field = $target };
    (@set $self:ident $target:ident $outer:ident . $inner:ident) => {
        $self.$outer.get_or_insert_with(Default::default).$inner = $target
    };
}

write_requests! {
    CreateBundleRequest.parent;
    ExportBundleRequest.name;
    MoveBundleRequest.name;
    UpdateBundleRequest.bundle.name, etag;
    CreateFragmentRequest.parent;
    UpdateFragmentRequest.fragment.name, etag;
    CreateCommandRequest.parent;
    UpdateCommandRequest.command.name, etag;
    UpdateConfigRequest.name, etag;
    EditConfigRequest.name, etag;
    CreateSkillRequest.parent;
    UpdateMcpServerRequest.mcp_server.name, etag;
    CreateProfileRequest.parent;
    UpdateProfileRequest.profile.name, etag;
    CreateCompanionRequest.parent;
    EnableCompanionRequest.name;
    DisableCompanionRequest.name;
    SetDefaultAgentRequest.parent;
    CreateAgentRequest.parent;
    UpdateAgentRequest.agent.name, etag;
    ImportBundleRequest.parent, etag;
    DeleteBundleRequest.name, etag;
    DeleteFragmentRequest.name, etag;
    DeleteCommandRequest.name, etag;
    DeleteSkillRequest.name, etag;
    DeleteProfileRequest.name, etag;
    DeleteAgentRequest.name, etag;
    DeleteCompanionRequest.name, etag;
    CleanRequest.project, etag;
    MaterializeRequest.project, etag;
    UninstallManageRequest.project, etag;
    RunDoctorRequest.project, etag;
    GetAuthStatusRequest.project;
    CheckManageRequest.project;
    GetInitPromptRequest.project;
    InstallGitignoreRequest.project, etag;
    InitProjectRequest.name, etag;
    InstallManageRequest.project, etag;
    DistillFragmentRequest.name;
    DistillCommandRequest.name;
    CreateRemoteRequest.parent;
    UpdateRemoteRequest.remote.name, etag;
    DeleteRemoteRequest.name, etag;
    SetDefaultRemoteRequest.parent;
    PullDependenciesRequest.parent;
    UpgradeDependenciesRequest.parent;
}
