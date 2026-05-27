import { makeStyles, mergeClasses, tokens } from '@fluentui/react-components';
import { Checkmark12Filled } from '@fluentui/react-icons';

/**
 * Bespoke wizard stepper. Fluent v9 doesn't ship a Stepper component
 * (Wizard exists only in v8 and is being phased out). Designed to match
 * the Claude Design prototype's create-wizard step list.
 */

const useStyles = makeStyles({
  root: {
    display: 'flex',
    gap: tokens.spacingHorizontalM,
    flexWrap: 'wrap',
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
  },
  step: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  stepActive: {
    color: tokens.colorNeutralForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  stepDone: { color: tokens.colorNeutralForeground2 },
  num: {
    width: '20px',
    height: '20px',
    borderRadius: tokens.borderRadiusCircular,
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
    display: 'grid',
    placeItems: 'center',
    fontSize: tokens.fontSizeBase100,
    fontWeight: tokens.fontWeightSemibold,
    flexShrink: 0,
  },
  numActive: {
    backgroundColor: tokens.colorBrandBackground,
    color: tokens.colorNeutralForegroundOnBrand,
  },
  numDone: {
    backgroundColor: tokens.colorPaletteGreenBackground2,
    color: tokens.colorPaletteGreenForeground2,
  },
});

interface StepperProps {
  step: number;
  steps: string[];
}

export default function Stepper({ step, steps }: StepperProps) {
  const styles = useStyles();
  return (
    <div className={styles.root}>
      {steps.map((label, i) => {
        const isActive = i === step;
        const isDone = i < step;
        return (
          <div
            key={i}
            className={mergeClasses(
              styles.step,
              isActive && styles.stepActive,
              isDone && styles.stepDone
            )}
          >
            <span
              className={mergeClasses(
                styles.num,
                isActive && styles.numActive,
                isDone && styles.numDone
              )}
            >
              {isDone ? <Checkmark12Filled /> : i + 1}
            </span>
            {label}
          </div>
        );
      })}
    </div>
  );
}
