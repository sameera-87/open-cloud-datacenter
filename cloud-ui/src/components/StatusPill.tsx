import { makeStyles, mergeClasses, tokens } from '@fluentui/react-components';

/**
 * Bespoke pill matching the Claude Design prototype: a small dot in the
 * status colour, then the label. Fluent v9 has `Badge`, but its
 * appearance options don't quite match the prototype's flat-with-dot
 * style and the colour palette is more saturated than we want.
 *
 * Statuses align with dc-api's ResourceStatus enum
 * (PENDING/ACTIVE/FAILED/DELETING) plus a few aliases the UI surfaces
 * for clarity (e.g. "Available" for images that have no PENDING phase).
 */

export type Status =
  | 'ACTIVE'
  | 'PENDING'
  | 'FAILED'
  | 'DELETING'
  | 'STOPPED'
  | 'AVAILABLE'
  | 'UPDATING';

const useStyles = makeStyles({
  pill: {
    display: 'inline-flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalXS,
    paddingTop: '2px',
    paddingBottom: '2px',
    paddingLeft: tokens.spacingHorizontalS,
    paddingRight: tokens.spacingHorizontalS,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    lineHeight: 1.4,
    whiteSpace: 'nowrap',
  },
  dot: {
    width: '8px',
    height: '8px',
    borderRadius: tokens.borderRadiusCircular,
    flexShrink: 0,
  },
  active: {
    backgroundColor: tokens.colorPaletteGreenBackground1,
    color: tokens.colorPaletteGreenForeground2,
  },
  activeDot: { backgroundColor: tokens.colorPaletteGreenForeground1 },
  pending: {
    backgroundColor: tokens.colorPaletteYellowBackground1,
    color: tokens.colorPaletteDarkOrangeForeground1,
  },
  pendingDot: { backgroundColor: tokens.colorPaletteDarkOrangeForeground1 },
  failed: {
    backgroundColor: tokens.colorPaletteRedBackground1,
    color: tokens.colorPaletteRedForeground1,
  },
  failedDot: { backgroundColor: tokens.colorPaletteRedForeground1 },
  neutral: {
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
  },
  neutralDot: { backgroundColor: tokens.colorNeutralForeground3 },
  info: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
  },
  infoDot: { backgroundColor: tokens.colorBrandForeground1 },
});

const labels: Record<Status, string> = {
  ACTIVE: 'Active',
  PENDING: 'Creating',
  FAILED: 'Failed',
  DELETING: 'Deleting',
  STOPPED: 'Stopped',
  AVAILABLE: 'Available',
  UPDATING: 'Updating',
};

interface StatusPillProps {
  status: Status | string;
}

export default function StatusPill({ status }: StatusPillProps) {
  const styles = useStyles();
  const upper = status.toUpperCase() as Status;

  const variant: { pill: string; dot: string } = (() => {
    switch (upper) {
      case 'ACTIVE':
      case 'AVAILABLE':
        return { pill: styles.active, dot: styles.activeDot };
      case 'PENDING':
      case 'UPDATING':
        return { pill: styles.pending, dot: styles.pendingDot };
      case 'FAILED':
        return { pill: styles.failed, dot: styles.failedDot };
      case 'DELETING':
        return { pill: styles.info, dot: styles.infoDot };
      case 'STOPPED':
      default:
        return { pill: styles.neutral, dot: styles.neutralDot };
    }
  })();

  return (
    <span className={mergeClasses(styles.pill, variant.pill)}>
      <span className={mergeClasses(styles.dot, variant.dot)} />
      {labels[upper] ?? status}
    </span>
  );
}
