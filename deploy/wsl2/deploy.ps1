param(
    [string]$Distribution = 'Ubuntu-24.04',
    [ValidateSet('all', 'backend', 'frontend')]
    [string]$Service = 'all',
    [string]$FrontendPath = ''
)
$ErrorActionPreference = 'Stop'
$sourceRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$resolvedLinuxRoot = & wsl.exe -d $Distribution -u root -- wslpath -a ($sourceRoot.Replace('\','/'))
if ($LASTEXITCODE -ne 0 -or -not $resolvedLinuxRoot) { throw '无法解析 WSL2 工作区路径' }
$linuxRoot = $resolvedLinuxRoot.Trim()
$deployArguments = @($Service)
if ($FrontendPath -and $Service -ne 'backend') {
    $frontendRoot = (Resolve-Path -LiteralPath $FrontendPath).Path
    $resolvedFrontend = & wsl.exe -d $Distribution -u root -- wslpath -a ($frontendRoot.Replace('\','/'))
    if ($LASTEXITCODE -ne 0 -or -not $resolvedFrontend) { throw '无法解析前端仓库路径' }
    $deployArguments += $resolvedFrontend.Trim()
}
& wsl.exe -d $Distribution -u root -- bash "$linuxRoot/deploy/wsl2/deploy.sh" @deployArguments
if ($LASTEXITCODE -ne 0) { throw 'WSL2 Docker 部署失败，请检查构建日志' }
& (Join-Path $PSScriptRoot 'start.ps1') -Distribution $Distribution
$stateRoot = Join-Path $env:LOCALAPPDATA 'AiCat/AvatarMVP'
$access = & wsl.exe -d $Distribution -u root -- cat /opt/aicat/avatar-mvp/LOCAL_ACCESS.txt
if ($LASTEXITCODE -ne 0) { throw '无法读取本地登录信息' }
$accessPath = Join-Path $stateRoot 'LOCAL_ACCESS.txt'
Set-Content -LiteralPath $accessPath -Value $access -Encoding utf8
Write-Output "登录信息文件：$accessPath"
