import { makeStyles, shorthands, tokens } from '@fluentui/react-components';

// Shared chrome for tenant-scoped list pages (VMs, Clusters, NSGs, VNets,
// Bastions, Images, Members, Service Accounts). Each page would otherwise
// re-declare these byte-for-byte; centralising them keeps spacing and
// borders consistent without forcing every page through one rigid layout.
// Page-specific entries (notices, dialogForm, rolePill, etc.) still live
// in the page's own makeStyles call alongside its useListPageStyles().
export const useListPageStyles = makeStyles({
  root: {
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '1400px',
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  header: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  subtitle: { color: tokens.colorNeutralForeground3, fontWeight: 400 },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  tableCard: { padding: 0, overflow: 'hidden' },
  tableMonoCell: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
  },
  tableMutedCell: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
  rowClickable: {
    cursor: 'pointer',
    ':hover': { backgroundColor: tokens.colorNeutralBackground1Hover },
  },
  nameLink: {
    color: tokens.colorBrandForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
});
