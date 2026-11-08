# apps fixtures

- `binary-Info.plist`: the Info.plist of a fake app, in binary form, as most
  shipped apps have it. `internal/apps` tests copy it into a bundle in a temp
  folder to prove binary plists are read. Source (XML) is `Info.xml`; rebuild
  with `plutil -convert binary1 -o binary-Info.plist Info.xml`.
- `agent.plist`: a LaunchAgent whose KeepAlive is a dictionary of conditions
  and whose program is in ProgramArguments.
