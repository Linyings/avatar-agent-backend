$ErrorActionPreference = 'Stop'
$reviewRoot = $PSScriptRoot
$projectRoot = (Resolve-Path (Join-Path $reviewRoot '../../..')).Path
$frontendRoot = Join-Path $projectRoot 'frontend'
Push-Location $frontendRoot
try {
    & npm.cmd test -- --run src/features/agent/StudioHome.test.tsx src/features/agent/agentStream.test.ts src/features/agent/studioService.test.ts src/shared/i18n/I18nProvider.dom.test.tsx src/shared/i18n/I18nProvider.test.ts --reporter=json --outputFile (Join-Path $reviewRoot 'fix-frontend-results.json')
    $reviewExit = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $reviewExit
