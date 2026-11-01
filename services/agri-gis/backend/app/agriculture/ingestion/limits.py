from dataclasses import asdict, dataclass


class IngestionDenied(ValueError):
    """Fixed safe code only. Never include input paths, URLs or parser messages."""

    @property
    def status_code(self):
        return (
            503
            if str(self)
            in {
                "parser_sandbox_unavailable",
                "parser_cleanup_failed",
                "source_dns_unavailable",
                "source_policy_unavailable",
            }
            else 400
        )


@dataclass(frozen=True)
class ParseLimits:
    input_bytes: int = 16 * 1024 * 1024
    expanded_bytes: int = 64 * 1024 * 1024
    members: int = 1000
    ratio: int = 100
    pixels: int = 16_000_000
    bands: int = 16
    features: int = 10000
    coordinates: int = 1_000_000
    memory_bytes: int = 768 * 1024 * 1024
    cpu_seconds: int = 10
    wall_seconds: int = 15
    output_bytes: int = 65536

    def __post_init__(self):
        ceilings = dict(
            input_bytes=64 * 1024 * 1024,
            expanded_bytes=256 * 1024 * 1024,
            members=10000,
            ratio=100,
            pixels=50_000_000,
            bands=32,
            features=100000,
            coordinates=2_000_000,
            memory_bytes=2 * 1024**3,
            cpu_seconds=60,
            wall_seconds=90,
            output_bytes=65536,
        )
        for name, value in asdict(self).items():
            if type(value) is not int or not 1 <= value <= ceilings[name]:
                raise IngestionDenied("invalid_parser_limits")
        if self.memory_bytes < 128 * 1024 * 1024:
            raise IngestionDenied("invalid_parser_limits")

    def wire(self):
        return asdict(self)
