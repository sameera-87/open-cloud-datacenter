/**
 * Shared size-card grid styles used by both:
 *   - WorkerPoolForm  (worker pool size selection)
 *   - ClusterCreateDrawer step 1  (system pool size selection)
 *
 * Single source of truth — visual identity is intentionally identical.
 */
import { makeStyles, shorthands, tokens } from '@fluentui/react-components';

export const useSizeCardStyles = makeStyles({
  sizeGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: tokens.spacingHorizontalM,
  },
  sizeCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    transition: 'border-color 0.1s, background-color 0.1s',
    ':hover': { ...shorthands.borderColor(tokens.colorBrandStroke1) },
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
  },
  sizeCardSelected: {
    ...shorthands.borderColor(tokens.colorBrandStroke1),
    backgroundColor: tokens.colorBrandBackground2,
  },
  sizeCardName: { fontWeight: tokens.fontWeightSemibold },
  sizeCardSpecs: { fontSize: tokens.fontSizeBase200, color: tokens.colorNeutralForeground2 },
});
