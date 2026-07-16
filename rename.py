import os

replacements = [
    ("github.com/omniswitch-dev/omniswitch/internal", "github.com/omniswitch-dev/omniswitch/internal"),
    ("github.com/omniswitch-dev/omniswitch/pkg", "github.com/omniswitch-dev/omniswitch/pkg"),
    ("module github.com/omniswitch-dev/omniswitch", "module github.com/omniswitch-dev/omniswitch"),
    ("OMNISWITCH_", "OMNISWITCH_"),
    ("x-omniswitch-", "x-omniswitch-"),
    ("X-Omniswitch-", "X-Omniswitch-"),
    ("omniswitch.dev/v1", "omniswitch.dev/v1"),
    ("cmd/omniswitch", "cmd/omniswitch"),
    (".omniswitch", ".omniswitch"),
    ("omniswitch-gateway", "omniswitch-gateway"),
    ("omniswitch-api-key", "omniswitch-api-key"),
    ("OmniSwitch", "OmniSwitch"),
    ("omniswitch proxy", "omniswitch proxy"),
    ("omniswitch validate", "omniswitch validate"),
    ("omniswitch trace", "omniswitch trace"),
    ("omniswitch replay", "omniswitch replay"),
    ("omniswitch diff", "omniswitch diff"),
    ('NewRootCommand("omniswitch")', 'NewRootCommand("omniswitch")'),
    ("github.com/omniswitch-dev/omniswitch", "github.com/omniswitch-dev/omniswitch")
]

def rename_in_file(filepath):
    try:
        with open(filepath, 'r', encoding='utf-8') as f:
            content = f.read()
            
        new_content = content
        for old, new in replacements:
            new_content = new_content.replace(old, new)
            
        if new_content != content:
            with open(filepath, 'w', encoding='utf-8') as f:
                f.write(new_content)
            print(f"Updated {filepath}")
    except Exception as e:
        pass

for root, dirs, files in os.walk("."):
    if ".git" in root or "website" in root:
        continue
    for file in files:
        if file.endswith((".go", ".md", ".yaml", ".yml", ".json", ".py", "go.mod", "Dockerfile", ".example", ".txt", "Makefile", "docker-compose.yml")):
            rename_in_file(os.path.join(root, file))

# Rename directories/files if they contain sentinel
import shutil

if os.path.exists("cmd/omniswitch"):
    shutil.move("cmd/omniswitch", "cmd/omniswitch")
    print("Renamed cmd/omniswitch to cmd/omniswitch")

if os.path.exists("sdk/python/sentinel.py"):
    shutil.move("sdk/python/sentinel.py", "sdk/python/omniswitch.py")
    print("Renamed sdk/python/sentinel.py to sdk/python/omniswitch.py")
