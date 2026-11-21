//! A write PLANNED, never performed (step 10b U3): the library opened over
//! ports that record what a use case asks them to write, so the daemon can
//! intend the change (its paths and new ids) before it makes it. Reads pass
//! through; a write is recorded and answered as the real port would answer
//! it now. What was recorded is then shaped into the write's one step and
//! made by an atomic operation (step 10b U8, `ctxloom_ports::Operations`),
//! never through these ports.

use std::sync::{Arc, Mutex, MutexGuard};

use ctxloom_ports::{
    Change, ConfigFiles, ContentRepo, Failure, LayerText, Put, Scope, Source, TreeFiles, UserFiles,
};

use crate::project::Library;

/// One write a use case asked for, in the order it asked.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Planned {
    /// A new bundle's directory, with the files written into it.
    NewBundle { bundle: String, files: Vec<Put> },
    /// Changes to an existing bundle's tree, in order.
    Changed {
        bundle: String,
        changes: Vec<Change>,
    },
    /// A bundle's whole tree taken away.
    BundleGone { bundle: String },
    /// A config layer written whole.
    Layer { scope: Scope, text: String },
    /// A bundle moved whole into ANOTHER tree (a local `bundle move`; step
    /// 10b QUESTIONS 1(a)), whose `.ctxloom` is `app_dir`.
    MovedInto { bundle: String, app_dir: String },
    /// Files written below the tree's `.ctxloom` by its scaffold (step 10c
    /// U5: `manage gitignore install`'s `.gitignore`), each relative to it.
    TreeFiles { files: Vec<Put> },
    /// A tree's `.ctxloom` written whole where it was not (step 10c U6: a
    /// first init's scaffold), each file relative to it.
    NewTree { files: Vec<Put> },
    /// A tree written OUTSIDE ctxloom's trees (`bundle export`; D2's
    /// EXCEPTION: staged beside it and renamed in NOREPLACE, no intent on
    /// any stream), its directory as the use case named it.
    Outside { dir: String, files: Vec<Put> },
}

/// What a library opened by [`Library::recording`] was asked to write.
#[derive(Clone, Default)]
pub struct Recorder(Arc<Mutex<Vec<Planned>>>);

impl Recorder {
    fn lock(&self) -> MutexGuard<'_, Vec<Planned>> {
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    /// Everything recorded, in order, taken.
    pub fn take(&self) -> Vec<Planned> {
        std::mem::take(&mut *self.lock())
    }

    fn claim(&self, bundle: &str) {
        self.lock().push(Planned::NewBundle {
            bundle: bundle.to_string(),
            files: Vec::new(),
        });
    }

    fn apply(&self, bundle: &str, changes: &[Change]) {
        let mut planned = self.lock();
        let new = planned.iter_mut().rev().find_map(|p| match p {
            Planned::NewBundle { bundle: b, files } if b == bundle => Some(files),
            _ => None,
        });
        if let Some(files) = new {
            for change in changes {
                match change {
                    Change::Write(put) => {
                        files.retain(|f| f.path != put.path);
                        files.push(put.clone());
                    }
                    Change::Remove { path } => files.retain(|f| &f.path != path),
                    Change::RemoveDir { path } => {
                        let under = format!("{}/", path.trim_end_matches('/'));
                        files.retain(|f| !f.path.starts_with(&under));
                    }
                }
            }
            return;
        }
        match planned.last_mut() {
            Some(Planned::Changed {
                bundle: b,
                changes: was,
            }) if b == bundle => {
                was.extend(changes.iter().cloned());
            }
            _ => planned.push(Planned::Changed {
                bundle: bundle.to_string(),
                changes: changes.to_vec(),
            }),
        }
    }

    fn remove_bundle(&self, bundle: &str) {
        let mut planned = self.lock();
        let claimed = planned
            .iter()
            .any(|p| matches!(p, Planned::NewBundle { bundle: b, .. } if b == bundle));
        // A claim given back (the use case's own undo) is no write at all.
        planned.retain(|p| match p {
            Planned::NewBundle { bundle: b, .. } | Planned::Changed { bundle: b, .. } => {
                b != bundle
            }
            _ => true,
        });
        if !claimed {
            planned.push(Planned::BundleGone {
                bundle: bundle.to_string(),
            });
        }
    }

    /// Whether the plan takes the bundle `bundle` away.
    fn gone(&self, bundle: &str) -> bool {
        self.lock()
            .iter()
            .any(|p| matches!(p, Planned::BundleGone { bundle: b } if b == bundle))
    }

    fn layer(&self, scope: Scope, text: &str) {
        let mut planned = self.lock();
        planned.retain(|p| !matches!(p, Planned::Layer { scope: s, .. } if *s == scope));
        planned.push(Planned::Layer {
            scope,
            text: text.to_string(),
        });
    }

    fn tree_files(&self, files: &[Put]) {
        self.lock().push(Planned::TreeFiles {
            files: files.to_vec(),
        });
    }

    fn new_tree(&self, files: &[Put]) {
        self.lock().push(Planned::NewTree {
            files: files.to_vec(),
        });
    }

    fn outside(&self, dir: &str, files: &[Put]) {
        self.lock().push(Planned::Outside {
            dir: dir.to_string(),
            files: files.to_vec(),
        });
    }

    /// The text a layer was planned to be given, if any.
    fn layer_text(&self, scope: Scope) -> Option<String> {
        self.lock().iter().find_map(|p| match p {
            Planned::Layer { scope: s, text } if *s == scope => Some(text.clone()),
            _ => None,
        })
    }
}

/// The content, its writes recorded.
struct RecordingContent {
    inner: Arc<dyn ContentRepo>,
    recorder: Recorder,
}

impl ContentRepo for RecordingContent {
    fn has_root(&self) -> Result<bool, Failure> {
        self.inner.has_root()
    }

    fn bundle_names(&self) -> Result<Vec<String>, Failure> {
        self.inner.bundle_names()
    }

    fn read_tree(&self, name: &str) -> Result<TreeFiles, Failure> {
        self.inner.read_tree(name)
    }

    fn manifest_path(&self, name: &str) -> String {
        self.inner.manifest_path(name)
    }

    fn apply(&self, name: &str, changes: &[Change]) -> Result<(), Failure> {
        self.recorder.apply(name, changes);
        Ok(())
    }

    /// Answered as the claim would be now: made, unless a bundle is there
    /// (the domain's `ResourceExists` and the operation judge a race).
    fn claim(&self, name: &str) -> Result<bool, Failure> {
        // A bundle this plan already took away is free to claim again (a
        // forced import: the old one gone, then the new one made).
        let there =
            self.inner.bundle_names()?.iter().any(|b| b == name) && !self.recorder.gone(name);
        if !there {
            self.recorder.claim(name);
        }
        Ok(!there)
    }

    fn remove_bundle(&self, name: &str) -> Result<(), Failure> {
        self.recorder.remove_bundle(name);
        Ok(())
    }

    fn bundle_dir(&self, name: &str) -> String {
        self.inner.bundle_dir(name)
    }

    fn bundle_dir_in(&self, app_dir: &str, name: &str) -> String {
        self.inner.bundle_dir_in(app_dir, name)
    }

    /// Answered moved: the operation's NOREPLACE rename judges a race.
    fn move_into(&self, name: &str, app_dir: &str) -> Result<bool, Failure> {
        self.recorder.lock().push(Planned::MovedInto {
            bundle: name.to_string(),
            app_dir: app_dir.to_string(),
        });
        Ok(true)
    }
}

/// The config files, their writes recorded; a layer read after its
/// planned write reads what was planned.
struct RecordingConfig {
    inner: Arc<dyn ConfigFiles>,
    recorder: Recorder,
}

impl ConfigFiles for RecordingConfig {
    fn source(&self) -> Source {
        self.inner.source()
    }

    fn app_dir(&self) -> String {
        self.inner.app_dir()
    }

    fn layers(&self) -> Result<Vec<LayerText>, Failure> {
        self.inner.layers()
    }

    fn layer(&self, scope: Scope) -> Result<Option<LayerText>, Failure> {
        match self.recorder.layer_text(scope) {
            Some(text) => Ok(Some(LayerText {
                path: self.inner.layer_path(scope),
                text,
            })),
            None => self.inner.layer(scope),
        }
    }

    fn layer_path(&self, scope: Scope) -> String {
        self.inner.layer_path(scope)
    }

    fn write_layer(&self, scope: Scope, text: &str) -> Result<(), Failure> {
        self.recorder.layer(scope, text);
        Ok(())
    }

    fn project_layer_path(&self) -> Option<String> {
        self.inner.project_layer_path()
    }

    fn ignored_root(&self) -> Option<String> {
        self.inner.ignored_root()
    }
}

/// The scaffold, its writes recorded (step 10c U5); its reads pass
/// through.
struct RecordingScaffold {
    inner: Arc<dyn ctxloom_ports::Scaffold>,
    recorder: Recorder,
}

impl ctxloom_ports::Scaffold for RecordingScaffold {
    fn app_dir(&self) -> String {
        self.inner.app_dir()
    }

    fn exists(&self, rel: &str) -> Result<bool, Failure> {
        self.inner.exists(rel)
    }

    fn read(&self, rel: &str) -> Result<Option<Vec<u8>>, Failure> {
        self.inner.read(rel)
    }

    fn is_new(&self) -> Result<bool, Failure> {
        self.inner.is_new()
    }

    /// A tree's own files, or (its directory not there yet, step 10c U6)
    /// the tree written whole.
    fn write(&self, files: &[Put]) -> Result<(), Failure> {
        if self.inner.is_new()? {
            self.recorder.new_tree(files);
        } else {
            self.recorder.tree_files(files);
        }
        Ok(())
    }
}

/// The user's files, a tree written outside the trees recorded; reads
/// pass through. A single file (a profile's or a skill's export) is not
/// served, and passes through.
struct RecordingUser {
    inner: Arc<dyn UserFiles>,
    recorder: Recorder,
}

impl UserFiles for RecordingUser {
    fn exists(&self, path: &str) -> Result<bool, Failure> {
        self.inner.exists(path)
    }

    fn is_dir(&self, path: &str) -> Result<bool, Failure> {
        self.inner.is_dir(path)
    }

    fn read_file(&self, path: &str) -> Result<Vec<u8>, Failure> {
        self.inner.read_file(path)
    }

    fn read_tree(&self, dir: &str) -> Result<TreeFiles, Failure> {
        self.inner.read_tree(dir)
    }

    fn write_file(&self, path: &str, bytes: &[u8], overwrite: bool) -> Result<bool, Failure> {
        self.inner.write_file(path, bytes, overwrite)
    }

    fn write_tree(&self, dir: &str, files: &[Put]) -> Result<(), Failure> {
        self.recorder.outside(dir, files);
        Ok(())
    }
}

impl Library {
    /// The tree `files` written outside ctxloom's trees, at `dir` (the
    /// daemon's make of a recorded [`Planned::Outside`]): whole or not at
    /// all, never over what is there (the user files' NOREPLACE).
    pub fn write_outside(&self, dir: &str, files: &[Put]) -> Result<(), crate::AppError> {
        let ports = self
            .ports
            .as_ref()
            .ok_or_else(|| crate::AppError::from(crate::WriteError::NotWired {}))?;
        ports
            .user
            .write_tree(dir, files)
            .map_err(crate::AppError::Cause)
    }

    /// The library whose writes are recorded, not made, and what records
    /// them: a use case run over it plans its write (step 10b U3).
    // The library holds its ports as `Arc<dyn ..>` without `Send`, so a
    // wrapper over them is one too.
    #[allow(clippy::arc_with_non_send_sync)]
    pub fn recording(mut self) -> (Library, Recorder) {
        let recorder = Recorder::default();
        self.content = Arc::new(RecordingContent {
            inner: Arc::clone(&self.content),
            recorder: recorder.clone(),
        });
        self.files = Arc::new(RecordingConfig {
            inner: Arc::clone(&self.files),
            recorder: recorder.clone(),
        });
        if let Some(ports) = self.ports.as_mut() {
            ports.user = Arc::new(RecordingUser {
                inner: Arc::clone(&ports.user),
                recorder: recorder.clone(),
            });
            ports.scaffold = Arc::new(RecordingScaffold {
                inner: Arc::clone(&ports.scaffold),
                recorder: recorder.clone(),
            });
        }
        (self, recorder)
    }

    /// The library whose editor round trips are `editor`'s (step 10b U3:
    /// the daemon's answers an Edit need; it launches nothing).
    pub fn editing_with(mut self, editor: Arc<dyn ctxloom_ports::Editor>) -> Library {
        if let Some(ports) = self.ports.as_mut() {
            ports.editor = editor;
        }
        self
    }

    /// `text` through this library's own editor, as a file named
    /// `file_name`: what a test's client does with a write's Edit need
    /// (the shipped CLI launches the need's EDITOR itself).
    #[cfg(any(test, feature = "test-support"))]
    pub fn edit_document(&self, file_name: &str, text: &str) -> Result<String, crate::AppError> {
        let ports = self
            .ports
            .as_ref()
            .ok_or_else(|| crate::AppError::from(crate::WriteError::NotWired {}))?;
        let command = crate::write::editor::editor_from_env(&self.environ);
        ports
            .editor
            .edit(&command, file_name, text)
            .map_err(crate::AppError::Cause)
    }

    /// The files below `dir` of the bundle `bundle` (`""`: the bundle's
    /// whole tree), each below it, sorted; none where there are none.
    pub fn bundle_files(&self, bundle: &str, dir: &str) -> Vec<String> {
        let Ok(tree) = self.content.read_tree(bundle) else {
            return Vec::new();
        };
        let under = match dir.trim_end_matches('/') {
            "" => String::new(),
            dir => format!("{dir}/"),
        };
        tree.keys()
            .filter_map(|path| path.strip_prefix(under.as_str()))
            .map(str::to_string)
            .collect()
    }
}

#[cfg(test)]
#[path = "recording.test.rs"]
mod tests;
