Lists the iOS Simulator devices of this Mac: UDID, state (Booted, Shutdown...), runtime and name, booted ones first. Without `all` it shows iOS devices and whatever is booted; with it, watchOS, tvOS and visionOS too.

The other Simulator* tools take a `device`: a UDID or a name from this list. Leave it out when exactly one device is booted. Only simulators of the machine this session runs on are reachable; with no Xcode the tools are not offered.

If the list is empty, no simulator runtime is installed. Installing one is a large download in Xcode's settings: tell the user, don't try it yourself.
