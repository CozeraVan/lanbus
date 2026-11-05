import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "src"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import pytest  # noqa: E402

from bus import Hub  # noqa: E402


@pytest.fixture(scope="module")
def hub():
    h = Hub()
    try:
        yield h
    finally:
        h.stop()