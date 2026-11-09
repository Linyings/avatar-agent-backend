#!/usr/bin/env python3
"""Build a compact copy of an installed image; never replace services or data.

The source is an image, not a container: mounted task data, models and database
are not copied or touched. The caller must verify and deploy the result before
retiring old images. Docker may retain source build layers until cache cleanup.
"""
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile


def inspect_image(name):
    return json.loads(subprocess.check_output(["docker", "image", "inspect", name]))[0]


def compact(source, destination):
    original = inspect_image(source)
    config = original["Config"]
    if config.get("Volumes") or config.get("OnBuild"):
        raise RuntimeError("Image declares volumes or build triggers; refusing automatic compaction")
    if not re.fullmatch(r"[A-Za-z0-9./:@_-]+", source):
        raise RuntimeError("Invalid source image reference")
    lines = [f"FROM {source} AS installed", "FROM scratch", "COPY --from=installed / /"]
    for entry in config.get("Env", []):
        key, value = entry.split("=", 1)
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key) or re.search(r"(KEY|TOKEN|PASSWORD|SECRET)$", key):
            raise RuntimeError("Unexpected environment variable in source image; inspect privately")
        if "$" in value or "\n" in value:
            raise RuntimeError("Environment requires manual escaping")
        lines.append(f"ENV {key}={json.dumps(value)}")
    for key in ("WorkingDir", "User", "StopSignal"):
        if config.get(key):
            if "\n" in config[key]:
                raise RuntimeError("Invalid image configuration")
            instruction = {"WorkingDir": "WORKDIR", "User": "USER", "StopSignal": "STOPSIGNAL"}[key]
            lines.append(f"{instruction} {config[key]}")
    for key in ("Shell", "Entrypoint", "Cmd"):
        if config.get(key):
            lines.append(f"{key.upper()} {json.dumps(config[key])}")
    for port in config.get("ExposedPorts", {}):
        lines.append(f"EXPOSE {port}")
    for key, value in (config.get("Labels") or {}).items():
        lines.append(f"LABEL {json.dumps(key)}={json.dumps(value)}")
    health = config.get("Healthcheck")
    if health:
        flags = []
        for key, flag in (("Interval", "interval"), ("Timeout", "timeout"), ("StartPeriod", "start-period"), ("StartInterval", "start-interval")):
            if health.get(key):
                flags.append(f"--{flag}={health[key]}ns")
        if health.get("Retries"):
            flags.append(f"--retries={health['Retries']}")
        test = health["Test"]
        command = "NONE" if test[0] == "NONE" else "CMD " + (test[1] if test[0] == "CMD-SHELL" else json.dumps(test[1:]))
        lines.append("HEALTHCHECK " + " ".join(flags + [command]))
    with tempfile.TemporaryDirectory(prefix="aicat-image-compact-") as temporary:
        Path(temporary, "Dockerfile").write_text("\n".join(lines) + "\n")
        subprocess.run(["docker", "build", "-t", destination, temporary], check=True)
    result = inspect_image(destination)
    if inspect_image(source)["Id"] != original["Id"]:
        raise RuntimeError("Source image changed during compaction; verify the build before using it")
    for key in ("User", "WorkingDir", "Entrypoint", "Cmd", "ExposedPorts", "Healthcheck", "Labels", "StopSignal", "Shell"):
        if config.get(key) != result["Config"].get(key):
            raise RuntimeError(f"Configuration mismatch: {key}; original image remains available")
    if sorted(config.get("Env", [])) != sorted(result["Config"].get("Env", [])):
        raise RuntimeError("Environment mismatch; original image remains available")
    print(json.dumps({"source": original["Id"], "destination": result["Id"], "source_layers": len(original["RootFS"]["Layers"]), "destination_layers": len(result["RootFS"]["Layers"]), "configuration": "identical"}))


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("Usage: compact-runtime.py SOURCE_IMAGE DESTINATION_IMAGE")
    compact(sys.argv[1], sys.argv[2])
