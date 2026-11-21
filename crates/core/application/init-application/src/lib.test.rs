//! `init`. Go: `cli/init.feature` (release/0.7, f36e8dfb21).

use std::cell::RefCell;

use exit_code::Exit;

use crate::{prompt, PromptFailure, Prompts};

/// A catalog holding `text` as the setup interview, else refusing.
struct Catalog(Option<&'static str>, RefCell<u32>);

impl Prompts for Catalog {
    fn setup_interview(&self) -> Result<crate::PromptRead, PromptFailure> {
        *self.1.borrow_mut() += 1;
        self.0
            .map(|t| t.to_string().into())
            .ok_or_else(|| PromptFailure {
                cause: Box::new(deps_application::AppError::DefaultNeedsName),
                exit: Exit::REFUSED,
            })
    }
}

// init.feature: "Init prompt emits the whole setup interview body", as the
// project's catalog holds it (owner ruling 128), a line after it as Go
// prints it.
#[test]
fn the_prompt_is_the_catalog_s_setup_interview() {
    let catalog = Catalog(Some("THE BODY"), RefCell::default());
    assert_eq!(prompt(&catalog).expect("held").text, "THE BODY\n");
    assert_eq!(*catalog.1.borrow(), 1);
}

// One the catalog lacks is its refusal, exit and all: no fallback.
#[test]
fn a_prompt_the_catalog_lacks_is_refused() {
    let refused = prompt(&Catalog(None, RefCell::default())).expect_err("refused");
    assert_eq!(refused.exit, Exit::REFUSED);
    assert_eq!(refused.cause.code(), "deps-application-default-needs-name");
}

// A catalog that names no companion's guidance carries none.
#[test]
fn test_a_catalog_carries_no_companion_guidance_by_default() {
    let catalog = Catalog(Some("interview"), RefCell::new(0));
    assert!(catalog.companion_guidance().is_empty());
}
