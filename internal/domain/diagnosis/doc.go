// Package diagnosis reads a run's logs for what went wrong: the line that
// says why a step stopped, the level of every line, and where the first error
// is. Pure functions over text the engine already stored -- computed when a
// screen asks, so a better rule applies to every run ever recorded.
//
// WHERE THE CAUSE COMES FROM, in the order Why tries:
//
//  1. The attempt: the failed one that started last, the higher attempt on a
//     tie -- the rule the Slack alert names its step by. An attempt whose step
//     later passed did not stop the run.
//  2. Its log, read from the end: the last line that reads as an error. A
//     non-zero exit and a Traceback frame the story without telling it, so
//     they count only when nothing else does.
//  3. With no error line, the last thing the log said.
//  4. With no log, the engine's error for the step: an image that did not
//     pull, a timeout before the first line.
//  5. With no failed step, the run's own error: it died before any started.
//
// The Cause says which of these it came from, so a screen can say it too.
//
// Reading a line's level is a HEURISTIC (see LevelOf): a word is not a
// meaning. The real answer is a structured marker the step prints, and it
// belongs to the steps that can print one.
package diagnosis
