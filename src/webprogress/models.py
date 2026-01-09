from pydantic import BaseModel


class ClientPayload(BaseModel):
    #: Client's hostname
    hostname: str
    #: Client's username
    username: str
    #: Number of finished iterations
    progress: float | int
    # The number of expected iterations. If unspecified,
    # len(iterable) is used if possible. If float("inf") or as a last
    # resort, only basic progress statistics are displayed
    # (no ETA, no progressbar).
    # If `gui` is True and this parameter needs subsequent updating,
    # specify an initial arbitrary large positive number,
    # e.g. 9e9.
    total: float | int
    #: Prefix for the progressbar
    description: str
    #: Time elapsed since beginning of task
    elapsed: float
    #: String that will be used to define the unit of each iteration
    unit: str
    # If 1 or True, the number of iterations will be reduced/scaled
    # automatically and a metric prefix following the
    # International System of Units standard will be added
    # (kilo, mega, etc.) [default: False]. If any other non-zero
    # number, will scale `total` and `n`.
    unit_scale: bool
    #: Iteration rate.
    rate: float | int
    #: [default: 1000], ignored unless `unit_scale` is True.
    unit_divisor: float | int
    # The initial counter value. Useful when restarting a progress
    # bar [default: 0]. If using float, consider specifying `{n:.3f}`
    # or similar in `bar_format`, or specifying `unit_scale`.
    initial: float | int
    #: Bar colour (e.g. 'green', '#00ff00').
    colour: str
    #: Access key to webprogress server
    key: str
