"""Mark release wheels as platform-specific without claiming a CPython ABI."""
import os
from pathlib import Path
from setuptools import Distribution, setup
from wheel.bdist_wheel import bdist_wheel


class GatewayDistribution(Distribution):
    def has_ext_modules(self):
        return (Path(__file__).parent / "omniswitch" / "bin").is_dir()


class GatewayWheel(bdist_wheel):
    def get_tag(self):
        if self.distribution.has_ext_modules():
            platform = os.environ.get("OMNISWITCH_WHEEL_PLATFORM")
            if not platform:
                raise RuntimeError("Binary wheels must be built with scripts/build-python-wheels.py")
            return "py3", "none", platform
        return super().get_tag()


setup(distclass=GatewayDistribution, cmdclass={"bdist_wheel": GatewayWheel})
