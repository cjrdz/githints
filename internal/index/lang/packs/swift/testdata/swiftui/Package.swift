// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "Journal",
    targets: [
        .target(name: "JournalKit"),
        .executableTarget(name: "JournalApp", dependencies: ["JournalKit"]),
        .testTarget(name: "JournalKitTests", dependencies: ["JournalKit"]),
    ]
)
