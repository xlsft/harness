Installs an app on a booted simulator from its `.app` bundle. The bundle must be built for the simulator: with `xcodebuild` that is a destination like `-destination 'platform=iOS Simulator,id=<UDID>'`, and the bundle ends up under `Build/Products/<Configuration>-iphonesimulator/` in the derived data folder (pass `-derivedDataPath` to know where). A bundle built for a phone does not install.

A relative path is taken from the working directory. Installing an app that is already there replaces it and keeps its data. Then start it with SimulatorLaunch.
