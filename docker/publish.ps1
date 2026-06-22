# Build and publish AxonHub from an external-network machine.
#
# Servers inside the VPC should pull the same image through:
#   registry-vpc.cn-shanghai.aliyuncs.com/xiaoin/axonhub:<tag>

$projectName = "axonhub"
$namespace = "xiaoin"
$publicRegistry = "registry.cn-shanghai.aliyuncs.com"
$vpcRegistry = "registry-vpc.cn-shanghai.aliyuncs.com"
$tag = Get-Date -Format "yyyyMMdd-HHmm"

$publicImage = "${publicRegistry}/${namespace}/${projectName}"
$vpcImage = "${vpcRegistry}/${namespace}/${projectName}"

docker build -f Dockerfile --build-arg "AXONHUB_BUILD_VERSION=$tag" -t $projectName . --progress=plain
if ($LASTEXITCODE -ne 0) {
    Write-Host "Docker build failed. Exiting."
    exit 1
}

docker tag $projectName "${publicImage}:$tag"
docker tag $projectName "${publicImage}:latest"

docker push "${publicImage}:$tag"
if ($LASTEXITCODE -ne 0) {
    Write-Host "Docker push tag failed. Exiting."
    exit 1
}

docker push "${publicImage}:latest"
if ($LASTEXITCODE -ne 0) {
    Write-Host "Docker push latest failed. Exiting."
    exit 1
}

Write-Host "docker build tag : $tag, success!"
Write-Host "public image : ${publicImage}:$tag"
Write-Host "vpc image    : ${vpcImage}:$tag"
