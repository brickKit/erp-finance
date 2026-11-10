from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ClaimRequest(_message.Message):
    __slots__ = ("claim_id", "open_item_id", "amount")
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    OPEN_ITEM_ID_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    open_item_id: str
    amount: str
    def __init__(self, claim_id: _Optional[str] = ..., open_item_id: _Optional[str] = ..., amount: _Optional[str] = ...) -> None: ...

class RenewRequest(_message.Message):
    __slots__ = ("claim_id",)
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    def __init__(self, claim_id: _Optional[str] = ...) -> None: ...

class RenewResponse(_message.Message):
    __slots__ = ("claim",)
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    claim: Claim
    def __init__(self, claim: _Optional[_Union[Claim, _Mapping]] = ...) -> None: ...

class ConfirmRequest(_message.Message):
    __slots__ = ("claim_id",)
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    def __init__(self, claim_id: _Optional[str] = ...) -> None: ...

class ConfirmResponse(_message.Message):
    __slots__ = ("claim",)
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    claim: Claim
    def __init__(self, claim: _Optional[_Union[Claim, _Mapping]] = ...) -> None: ...

class ReleaseRequest(_message.Message):
    __slots__ = ("claim_id",)
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    def __init__(self, claim_id: _Optional[str] = ...) -> None: ...

class ReleaseResponse(_message.Message):
    __slots__ = ("claim",)
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    claim: Claim
    def __init__(self, claim: _Optional[_Union[Claim, _Mapping]] = ...) -> None: ...

class GetRequest(_message.Message):
    __slots__ = ("claim_id",)
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    def __init__(self, claim_id: _Optional[str] = ...) -> None: ...

class GetResponse(_message.Message):
    __slots__ = ("claim",)
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    claim: Claim
    def __init__(self, claim: _Optional[_Union[Claim, _Mapping]] = ...) -> None: ...

class Claim(_message.Message):
    __slots__ = ("claim_id", "open_item_id", "amount", "state", "expires_at", "formalized_amount", "reversed_amount")
    CLAIM_ID_FIELD_NUMBER: _ClassVar[int]
    OPEN_ITEM_ID_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    FORMALIZED_AMOUNT_FIELD_NUMBER: _ClassVar[int]
    REVERSED_AMOUNT_FIELD_NUMBER: _ClassVar[int]
    claim_id: str
    open_item_id: str
    amount: str
    state: str
    expires_at: str
    formalized_amount: str
    reversed_amount: str
    def __init__(self, claim_id: _Optional[str] = ..., open_item_id: _Optional[str] = ..., amount: _Optional[str] = ..., state: _Optional[str] = ..., expires_at: _Optional[str] = ..., formalized_amount: _Optional[str] = ..., reversed_amount: _Optional[str] = ...) -> None: ...

class ClaimResponse(_message.Message):
    __slots__ = ("claim",)
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    claim: Claim
    def __init__(self, claim: _Optional[_Union[Claim, _Mapping]] = ...) -> None: ...

class OpenItem(_message.Message):
    __slots__ = ("id", "kind", "legal_entity_id", "partner_id", "currency", "amount", "settled", "pay_claimed", "available", "due_date", "source_kind", "source_id", "source_number")
    ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    LEGAL_ENTITY_ID_FIELD_NUMBER: _ClassVar[int]
    PARTNER_ID_FIELD_NUMBER: _ClassVar[int]
    CURRENCY_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_FIELD_NUMBER: _ClassVar[int]
    SETTLED_FIELD_NUMBER: _ClassVar[int]
    PAY_CLAIMED_FIELD_NUMBER: _ClassVar[int]
    AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    DUE_DATE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_KIND_FIELD_NUMBER: _ClassVar[int]
    SOURCE_ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_NUMBER_FIELD_NUMBER: _ClassVar[int]
    id: str
    kind: str
    legal_entity_id: str
    partner_id: str
    currency: str
    amount: str
    settled: str
    pay_claimed: str
    available: str
    due_date: str
    source_kind: str
    source_id: str
    source_number: str
    def __init__(self, id: _Optional[str] = ..., kind: _Optional[str] = ..., legal_entity_id: _Optional[str] = ..., partner_id: _Optional[str] = ..., currency: _Optional[str] = ..., amount: _Optional[str] = ..., settled: _Optional[str] = ..., pay_claimed: _Optional[str] = ..., available: _Optional[str] = ..., due_date: _Optional[str] = ..., source_kind: _Optional[str] = ..., source_id: _Optional[str] = ..., source_number: _Optional[str] = ...) -> None: ...

class DueItemsRequest(_message.Message):
    __slots__ = ("legal_entity_id", "kind", "partner_id", "due_on_or_before", "cursor", "limit")
    LEGAL_ENTITY_ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    PARTNER_ID_FIELD_NUMBER: _ClassVar[int]
    DUE_ON_OR_BEFORE_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    legal_entity_id: str
    kind: str
    partner_id: str
    due_on_or_before: str
    cursor: str
    limit: int
    def __init__(self, legal_entity_id: _Optional[str] = ..., kind: _Optional[str] = ..., partner_id: _Optional[str] = ..., due_on_or_before: _Optional[str] = ..., cursor: _Optional[str] = ..., limit: _Optional[int] = ...) -> None: ...

class DueItemsResponse(_message.Message):
    __slots__ = ("items", "next_cursor")
    ITEMS_FIELD_NUMBER: _ClassVar[int]
    NEXT_CURSOR_FIELD_NUMBER: _ClassVar[int]
    items: _containers.RepeatedCompositeFieldContainer[OpenItem]
    next_cursor: str
    def __init__(self, items: _Optional[_Iterable[_Union[OpenItem, _Mapping]]] = ..., next_cursor: _Optional[str] = ...) -> None: ...

class ReadRequest(_message.Message):
    __slots__ = ("fact_ids",)
    FACT_IDS_FIELD_NUMBER: _ClassVar[int]
    fact_ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, fact_ids: _Optional[_Iterable[str]] = ...) -> None: ...

class Fact(_message.Message):
    __slots__ = ("fact_id", "type", "data")
    FACT_ID_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    fact_id: str
    type: str
    data: bytes
    def __init__(self, fact_id: _Optional[str] = ..., type: _Optional[str] = ..., data: _Optional[bytes] = ...) -> None: ...

class ReadResponse(_message.Message):
    __slots__ = ("facts",)
    FACTS_FIELD_NUMBER: _ClassVar[int]
    facts: _containers.RepeatedCompositeFieldContainer[Fact]
    def __init__(self, facts: _Optional[_Iterable[_Union[Fact, _Mapping]]] = ...) -> None: ...
