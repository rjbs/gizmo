Write the next unit of the Go learning booklet in `learning/`.

First, sync this branch with `main` by **merging** `main` into it; do not
rebase.  `go-learning` is an "editions" branch: each sync is a merge commit
that marks the `main` commit this edition of the text was reconciled against.
Note the branch tip first (`git rev-parse HEAD`) so you can review what the
merge pulled in.

Review the changes `main` brought in with that merge (`git diff
<old-tip>..HEAD`, or the merge's second-parent diff).  The booklet also reads
from three other repositories, `../TaskMaster`, `../jiggle` and `../Juke`.
`learning/SYLLABUS.md` records the commit of each that the text was last
reconciled against; look at what each has done since (`git log
<commit>..HEAD`).

If those changes suggest that `learning/SYLLABUS.md` should be updated for
future units, make those changes.  Update the table of commits in the
syllabus to the ones just reviewed.  Commit that.

If the changes make an already-written unit wrong, such as a file that moved,
line numbers in a code tour that no longer match, or an explanation of code
that is gone, fix the unit and update the commits it says it was written
against.  Commit that separately.

Then review the file `learning/questions.md`, which holds questions from the
reader during the last unit.  Merge the material from the questions into the
unit they came from, as part of the text and not as an appended Q&A.  Remove
the now-merged questions from the questions file.  Commit that.

Then write the next unit from the syllabus.  When doing so, reflect on the
questions and answers you just merged into the previous unit.  They show the
kind of thing the reader felt was missing from the text.  The unit must name
the commit of each repository it was written against.

Before committing, check the unit against the code: every file and line
range in the code tour, every claim about what the code does, and every
exercise, which you should try and then undo.

Commit the new unit.
