Two commands in `.chunk/config.json` can currently share a `name`, and nothing
says so: `chunk validate test` runs whichever `test` comes first and the other
is silently ignored. Make the project config reject commands like that.

- Loading or saving a project config (`config.LoadProjectConfig`,
  `config.SaveProjectConfig`) must fail when two commands have the same `name`
  (compared exactly, so `test` and `Test` are different commands), when a
  command's `name` is empty or only whitespace, or when a command's `timeout`
  is negative. For a duplicate name or a negative timeout the error must
  include the command's name in double quotes (e.g. `"test"`), as the existing
  local/remote conflict error does. Saving an invalid config must not write the
  file. Configs that are valid today must keep loading unchanged.
- `chunk validate` must refuse such a config with a non-zero exit, including
  `--dry-run` and `--list` (with or without `--json`). `--list` currently shows
  an empty list when `.chunk/config.json` exists but cannot be loaded (malformed
  JSON or an invalid config); it must fail instead and print nothing on stdout.
  With no config file at all, `--list` keeps its current output.
- `config.SaveCommand` (used by `chunk validate --cmd ... --save`) currently
  starts from an empty config whenever the existing one fails to load, which
  throws away every command in it. When `.chunk/config.json` exists but is
  malformed or invalid, it must return an error and leave the file byte-for-byte
  unchanged. When there is no config file it still creates one.

Add focused tests for these cases.
