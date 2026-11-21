# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

ctxloom-store-sqlite-per-project-store = this project still has a store of its own, { $path }, from before ctxloom kept every project's sessions in one store; it is not read, and nothing is imported from it
ctxloom-store-sqlite-per-project-store-fix = remove { $path } (its sessions' history is not carried over), then run the command again
ctxloom-store-sqlite-project-dir-unmade = could not make the project's directory { $path }: { $ext_err_txt }
