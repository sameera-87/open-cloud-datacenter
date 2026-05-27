/**
 * WorkerPoolForm — pure controlled form for a single worker pool entry.
 *
 * Consumed by:
 *   - ClusterCreateDrawer (step 2: Worker pools — inline add/edit form)
 *   - AddWorkerPoolDialog inside NodePoolsTab (post-create flow)
 *
 * All types, constants, and validation helpers live in workerPoolTypes.ts to
 * keep this file a pure-component module (react-refresh requirement).
 *
 * Size card styles are imported from useSizeCardStyles so both this form
 * and the ClusterCreateDrawer system-pool step share an identical card grid.
 */
import {
  Body1,
  Body2,
  Field,
  InfoLabel,
  Input,
  makeStyles,
  mergeClasses,
  Slider,
  Switch,
  tokens,
} from '@fluentui/react-components';
import { LabelEditor } from './LabelEditor';
import { TaintEditor } from './TaintEditor';
import { useSizeCardStyles } from './useSizeCardStyles';
import {
  SIZES,
  validateWorkerPoolForm,
  type WorkerPoolFormValue,
} from './workerPoolTypes';

const useStyles = makeStyles({
  form: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  sliderRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  sliderValue: {
    minWidth: '80px',
    fontWeight: tokens.fontWeightSemibold,
  },
  specsHint: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
    marginTop: tokens.spacingVerticalXS,
  },
});

interface WorkerPoolFormProps {
  value: WorkerPoolFormValue;
  onChange: (patch: Partial<WorkerPoolFormValue>) => void;
  /** Names already taken — used for duplicate check. Does NOT include this pool's own current name. */
  existingNames: string[];
  /** When true, show validation messages (activate after first submit attempt). */
  showErrors?: boolean;
}

export function WorkerPoolForm({
  value,
  onChange,
  existingNames,
  showErrors = false,
}: WorkerPoolFormProps) {
  const styles = useStyles();
  const cardStyles = useSizeCardStyles();
  const errors = showErrors ? validateWorkerPoolForm(value, existingNames) : {};

  return (
    <div className={styles.form}>
      <Field
        label="Pool name"
        required
        hint='Lowercase, hyphens allowed. Must start with a letter, max 40 chars. "system" is reserved.'
        validationState={errors.name ? 'error' : 'none'}
        validationMessage={errors.name}
      >
        <Input
          value={value.name}
          onChange={(_, d) =>
            onChange({ name: d.value.toLowerCase().replace(/[^a-z0-9-]/g, '') })
          }
          placeholder="workers-01"
          autoFocus
        />
      </Field>

      <div>
        <InfoLabel info="Each node in this pool uses this size profile." required>
          Node size
        </InfoLabel>
      </div>
      <div className={cardStyles.sizeGrid}>
        {SIZES.map((s) => (
          <div
            key={s.id}
            className={mergeClasses(
              cardStyles.sizeCard,
              value.size === s.id && cardStyles.sizeCardSelected,
            )}
            onClick={() => onChange({ size: s.id })}
            role="button"
            tabIndex={0}
            onKeyDown={(e) => e.key === 'Enter' && onChange({ size: s.id })}
          >
            <Body1 className={cardStyles.sizeCardName}>{s.label}</Body1>
            <Body2 className={cardStyles.sizeCardSpecs}>{s.specs}</Body2>
          </div>
        ))}
      </div>

      <Field
        label="Node count"
        hint="1 to 50 nodes."
        validationState={errors.count ? 'error' : 'none'}
        validationMessage={errors.count}
      >
        <div className={styles.sliderRow}>
          <Slider
            min={1}
            max={50}
            step={1}
            value={value.count}
            onChange={(_, d) => onChange({ count: d.value })}
            style={{ flex: 1 }}
          />
          <span className={styles.sliderValue}>
            {value.count} {value.count === 1 ? 'node' : 'nodes'}
          </span>
        </div>
      </Field>

      <Field label="Override disk size">
        <Switch
          checked={value.diskOverride}
          onChange={(_, d) => onChange({ diskOverride: d.checked })}
          label={value.diskOverride ? 'Custom disk size' : 'Use size default'}
        />
        {value.diskOverride && (
          <div className={styles.sliderRow} style={{ marginTop: tokens.spacingVerticalS }}>
            <Slider
              min={40}
              max={500}
              step={10}
              value={value.diskGb}
              onChange={(_, d) => onChange({ diskGb: d.value })}
              style={{ flex: 1 }}
            />
            <span className={styles.sliderValue}>{value.diskGb} GB</span>
          </div>
        )}
      </Field>

      <TaintEditor taints={value.taints} onChange={(taints) => onChange({ taints })} />
      <LabelEditor labels={value.labels} onChange={(labels) => onChange({ labels })} />
    </div>
  );
}
