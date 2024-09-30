allow_k8s_contexts('taji')
# Define the output directory for the binaries
output_dir = "build"

# Define the binary name
binary_name = "kubesense"

# Define the platforms you want to build for
platforms = [
    {"GOOS": "linux", "GOARCH": "amd64", "output": "linux-amd64"},
    {"GOOS": "linux", "GOARCH": "arm64", "output": "linux-arm64"},
    {"GOOS": "darwin", "GOARCH": "amd64", "output": "darwin-amd64"},
    {"GOOS": "darwin", "GOARCH": "arm64", "output": "darwin-arm64"},
    {"GOOS": "windows", "GOARCH": "amd64", "output": "windows-amd64.exe"},
]

# Build the Go binary for each platform
for platform in platforms:
    local_resource(
        allow_parallel=True,
        name=platform["GOOS"] +'-'+ platform["GOARCH"]   ,
        cmd='GOOS='+ platform["GOOS"] + ' GOARCH='+platform["GOARCH"] + ' go build -o build/kubesense-' + platform["output"] + ' ./',
        deps=['go.mod', 'go.sum', 'main.go'],  # Add all relevant Go source files and dependencies
        auto_init=False
    )

local_resource(
    name="upload_to_s3",
    cmd="echo 'temp'",
    auto_init=False
)