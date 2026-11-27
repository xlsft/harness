Starts an installed app on a booted simulator by its bundle identifier (`CFBundleIdentifier` of its Info.plist, such as `com.example.MyApp`; built-in apps have ids like `com.apple.Preferences`). By default an instance that already runs is stopped first, so the launch picks up a fresh install; set `restart` to false to bring the running one forward instead.

`args` are passed to the app as launch arguments. The result gives the process id. The app comes up after a moment: take a SimulatorScreenshot before acting on it.
