# One skill, every engine's own skill folder

A **command** is something you invoke — a slash template you type. An **Agent
Skill** is something the assistant picks up on its own, reading it when the work
looks relevant, without being asked.

Every engine supports skills. Every engine also expects them somewhere
different, and calls the folder something slightly different. Written by hand,
one skill means five copies drifting apart.

## Where it lands

You author it once. ctxloom puts it where each engine looks — and each engine
looks somewhere different, down to one of them spelling the folder `skill`
rather than `skills`. The full per-engine table is in the skill command's own
reference, which is where that claim is proven engine by engine.

## Whole, not just present

A skill that arrives as an empty file with the right name is worse than one that
does not arrive: the assistant finds it, reads nothing useful, and you have no
reason to suspect anything. So the body has to survive the trip intact, and so
does everything shipped alongside it.

That includes the **executable bit**. If your skill ships a `scripts/run.sh`,
it arrives runnable. A script delivered without its mode is a script that fails
the first time the assistant tries to use it, in a way that looks like the
script is broken rather than the delivery.

## Tamper-checking

Once you record a skill's manifest, ctxloom keeps a checksum and file mode for
every file in the tree. After that, drift between what was recorded and what is
on disk is caught at install time and **withheld loudly** rather than quietly
materialized.

Before that recording, a fresh read of the tree is trusted as-is. That is a
deliberate ordering, not an oversight — you can author freely, and you opt into
the integrity check when the skill is ready to travel.

## Curating

Shipping a skill in a bundle does not push it at every project. A profile
chooses which skills it carries, so a bundle can offer more than any one project
takes. You get the same authored skill in five engines' native formats, and only
the projects that asked for it.
