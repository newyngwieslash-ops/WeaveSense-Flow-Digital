"""Fixed no-skip cache/SSE gates; never publish raw Go diagnostics or tokens."""
import argparse
import importlib.util
import json
from pathlib import Path

spec = importlib.util.spec_from_file_location('strict_go_result', Path(__file__).with_name('identity-chain-result.py'))
strict = importlib.util.module_from_spec(spec)
spec.loader.exec_module(strict)

AGRICULTURE_ROOTS = {
    'TestScopedProjectionCacheActualAuthority',
    'TestScopedProjectionCacheCrossTenantAndVersionKeys',
    'TestScopedProjectionCacheActualTwoTenantSameInputs',
    'TestScopedProjectionRejectsWriteAndPostDescriptionRevocation',
    'TestScopedProjectionWaitingFillCannotReviveRevokedActor',
    'TestJobEventsRealSessionScopeReplayAndRevocation',
    'TestJobEventsCrossTenantAndDefaultUnavailable',
    'TestJobEventsEstablishedStreamStopsOnLogoutOrRevocation',
    'TestJobEventCursorIntegrityAndRestart',
    'TestJobEventsProviderCancellationAndMalformedBatch',
    'TestJobEventsEachWriteRechecksCurrentAuthority',
}
LEGACY_ROOTS = {'TestConnectedLegacyStreamsRevalidateAuthority',
                'TestTaskStreamRevocationDuringConnectedFlush',
                'TestTaskStreamRevocationBetweenBufferedEvents'}
CACHE_ROOTS = {'TestCachedTextReplayIsolatesUsersCursorsAndCopies'}
REQUIRED = {
    'agriculture': AGRICULTURE_ROOTS | {
        'TestJobEventsEstablishedStreamStopsOnLogoutOrRevocation/' + case
        for case in ('logout', 'membership', 'generation', 'disconnect', 'resource_version')
    } | {'TestJobEventsProviderCancellationAndMalformedBatch/' + case
         for case in ('provider_observes_request_cancellation', 'whole_batch_rejected_before_first_byte')}
      | {'TestJobEventsEachWriteRechecksCurrentAuthority/' + case for case in ('logout', 'membership')},
    'legacy': LEGACY_ROOTS | {
        'TestConnectedLegacyStreamsRevalidateAuthority/' + kind + '/' + change
        for kind in ('text', 'agent')
        for change in ('logout', 'expired', 'disabled', 'ownership', 'other_cookie', 'success')
    },
    'cache': CACHE_ROOTS,
}


def reduce(kind, raw):
    return strict.reduce(raw, REQUIRED[kind], 'scoped_cache_sse_' + kind)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--kind', choices=sorted(REQUIRED), required=True)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    try:
        result = reduce(args.kind, args.input.read_text())
    except (OSError, UnicodeError, ValueError):
        result = {'suite': 'scoped_cache_sse_' + args.kind, 'passed': False, 'reason': 'invalid_private_report'}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    raise SystemExit(0 if result['passed'] else 1)


if __name__ == '__main__':
    main()
