// 构建期 shim：antd 组件来自控制台实例（与主题/上下文一致），插件不自带。
const A = globalThis.__CORDIS_CONSOLE.antd;

export default A;
export const {
  Affix, Alert, AutoComplete, Avatar, Badge, Breadcrumb, Button, Calendar,
  Card, Carousel, Cascader, Checkbox, Col, Collapse, ConfigProvider,
  DatePicker, Descriptions, Divider, Drawer, Dropdown, Empty, Flex,
  FloatButton, Form, Image, Input, InputNumber, Layout, List, Menu, Modal,
  Pagination, Popconfirm, Popover, Progress, Radio, Rate, Result, Row,
  Segmented, Select, Skeleton, Slider, Space, Spin, Statistic, Steps,
  Switch, Table, Tabs, Tag, Timeline, Tooltip, Transfer, Tree, TreeSelect,
  Typography, Upload, theme,
} = A;
