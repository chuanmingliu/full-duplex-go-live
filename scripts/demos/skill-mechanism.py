#!/usr/bin/env python3
"""Run the local skill lifecycle demonstration with existing integration fixtures."""
import argparse
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / "services" / "skills-mcp"
STAGES = [
    (
        "Capture, idle scheduling, cron and restart recovery",
        SERVICE,
        "./internal/dreaming",
        ["TestAutomaticCaptureIdleReviewAndRestart", "TestReviewDefersWhenAgentBecomesBusy",
         "TestActivityUnknownAndIdleDelay", "TestCronOnlyDispatchSurvivesBusyAndRestart",
         "TestCronDispatchReceiptRecoversAfterRestart"],
    ),
    (
        "Model review, candidate validation, publication and MCP retrieval",
        SERVICE,
        ".",
        ["TestDreamToPublishedMCPSkill", "TestMemoryAuthorizationAndOptionalStartup"],
    ),
    (
        "Voice-host rendering, state freshness, failure handling and recording",
        ROOT,
        "./internal/duplex",
        ["TestSkillsActiveCallRendering", "TestSkillSlotEngineScenarios",
         "TestSkillSlotLiteralSubstitution", "TestSkillSlotModeReloadedOnNextRequest",
         "TestSkillSlotCatalogVersionRace", "TestSkillsExpireAtTurnAndMemoryBoundary",
         "TestSkillsNoMatchAndDeadlineFallback", "TestSkillsRejectChangedContentAndPublication",
         "TestSkillsInterruptedFetchDoesNotResumeAnswer", "TestSkillTraceMeasuresOnlyExecutedStages",
         "TestSessionRequestRecordingIncludesRenderedSkill"],
    ),
    (
        "Journal browsing of recorded sessions and exact request bytes",
        ROOT,
        "./internal/journal",
        ["TestRequestBrowserReadsExactBodyAndMetadata", "TestRequestBrowserHandlesUnscopedAndHashedSessions"],
    ),
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--race", action="store_true", help="enable Go's race detector")
    args = parser.parse_args()
    print("Skill lifecycle demo — synthetic fixtures, temporary storage, loopback HTTP.", flush=True)
    print("The reviewer and conversation provider are local stubs. Fixtures simulate owner approval.", flush=True)
    print("Each stage runs independently and exercises its production components.", flush=True)
    for title, directory, package, tests in STAGES:
        print("\n" + title, flush=True)
        command = ["go", "test", "-count=1", "-v", "-timeout=120s"]
        if args.race:
            command.append("-race")
        command.extend([package, "-run", "^(" + "|".join(tests) + ")$"])
        try:
            result = subprocess.run(command, cwd=directory, timeout=300, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            print(f"Demo could not finish: {error}", file=sys.stderr)
            return 1
        if result.returncode:
            return result.returncode
    print("\nAll stages passed. Follow docs/architecture/12-skill-mechanism.md for the protocol walkthrough.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
