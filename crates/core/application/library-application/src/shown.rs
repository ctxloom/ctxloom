//! Paths as the content port shows them: in the host's spelling, which a
//! path made from one keeps, so a person on Windows is shown `C:\p\x\y`,
//! never `C:\p\x/y`.

/// Whether `path` is spelled the Windows way: a drive (`C:`) or a share
/// (`\\srv`) at its start. Any other is spelled with `/`.
fn windows_spelled(path: &str) -> bool {
    path.starts_with(r"\\") || path.as_bytes().get(1) == Some(&b':')
}

/// The separator `path` is spelled with: `\` the Windows way, else `/`.
fn separator(path: &str) -> char {
    if windows_spelled(path) {
        '\\'
    } else {
        '/'
    }
}

/// `rel`, a `/`-separated path, below the shown directory `dir`, spelled as
/// `dir` is.
pub(crate) fn below(dir: &str, rel: &str) -> String {
    let separator = separator(dir);
    format!(
        "{dir}{separator}{}",
        rel.replace('/', &separator.to_string())
    )
}

/// The directory a shown `file` is in, at its last separator; the file
/// itself when it is in none.
pub(crate) fn parent(file: &str) -> &str {
    file.rfind(separator(file)).map_or(file, |at| &file[..at])
}

#[cfg(test)]
#[path = "shown.test.rs"]
mod tests;
